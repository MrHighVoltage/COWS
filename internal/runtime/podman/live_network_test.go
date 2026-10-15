//go:build podman_integration

package podman

import (
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/runtime"
)

// probeSource is a static helper mounted into the containers, so the network
// test does not depend on tools inside the configured image. Without
// arguments it serves its PROBE_NAME on the desktop port; "get URL" exits
// non-zero when the URL is unreachable.
const probeSource = `package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == "get" {
		client := http.Client{Timeout: 3 * time.Second}
		response, err := client.Get(os.Args[2])
		if err != nil {
			os.Exit(1)
		}
		response.Body.Close()
		return
	}
	name := os.Getenv("PROBE_NAME")
	_ = http.ListenAndServe(":5901", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, name) }))
}
`

func TestLivePodmanNetworkIsolation(t *testing.T) {
	ctx, adapter, image, dataRoot := livePodman(t)
	probeDir := filepath.Join(dataRoot, "probe")
	if err := os.Mkdir(probeDir, 0o755); err != nil {
		t.Fatalf("create probe directory: %v", err)
	}
	// The probe must be readable by container users that the private user
	// namespace maps away from the host account owning the file.
	if err := os.Chmod(probeDir, 0o755); err != nil {
		t.Fatalf("open probe directory: %v", err)
	}
	probeFile := filepath.Join(dataRoot, "probe.go")
	if err := os.WriteFile(probeFile, []byte(probeSource), 0o600); err != nil {
		t.Fatalf("write probe source: %v", err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(probeDir, "probe"), probeFile)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build network probe: %v\n%s", err, output)
	}

	type liveWorkspace struct {
		name     string
		user     *runtime.ContainerUser
		id       string
		network  string
		hostPort int
		runtime  string
		address  string
	}
	// Templates with a container_user use the Libpod create path; others use
	// the compatibility path. Both must honor the workspace network.
	workspaces := []*liveWorkspace{
		{name: "configured user", user: &runtime.ContainerUser{Username: "alice", UID: 2000, GID: 2000, Name: "Alice", Home: "/home/alice", Shell: "/bin/sh", PasswdEntry: "alice:x:2000:2000:Alice:/home/alice:/bin/sh"}},
		{name: "image identity"},
	}
	for _, workspace := range workspaces {
		workspace.id = liveWorkspaceID(t)
		workspace.network = "cows-net-" + workspace.id
		workspace.hostPort = freeLoopbackPort(t)
		if err := adapter.CreateWorkspaceNetwork(ctx, workspace.network); err != nil {
			t.Fatalf("%s: create workspace network: %v", workspace.name, err)
		}
		network := workspace.network
		t.Cleanup(func() { _ = adapter.RemoveWorkspaceNetwork(ctx, network) })
		handle, err := adapter.CreateWorkspace(ctx, runtime.WorkspaceSpec{
			WorkspaceID: workspace.id,
			Image:       runtime.Image{Reference: image},
			Limits:      runtime.ResourceLimits{CPUMillis: 500, MemoryBytes: 256 << 20},
			Command:     []string{"/opt/probe/probe"},
			Environment: []runtime.EnvironmentVariable{{Name: "PROBE_NAME", Value: workspace.id}},
			User:        workspace.user,
			Mounts:      []runtime.Mount{{Name: "probe", Type: "bind", Source: probeDir, ContainerPath: "/opt/probe", ReadOnly: true}},
			Ports:       []runtime.PortBinding{{ServiceName: "desktop", Protocol: "tcp", ContainerPort: 5901, HostPort: workspace.hostPort, HostIP: "127.0.0.1"}},
			NetworkMode: workspace.network,
		})
		if err != nil {
			t.Fatalf("%s: create workspace: %v", workspace.name, err)
		}
		workspace.runtime = handle.RuntimeID
		t.Cleanup(func() { _ = exec.Command("podman", "rm", "--force", "--time", "0", handle.RuntimeID).Run() })
		if err := adapter.StartWorkspace(ctx, handle.RuntimeID); err != nil {
			t.Fatalf("%s: start workspace: %v", workspace.name, err)
		}
	}

	for _, workspace := range workspaces {
		attached := strings.Fields(podmanCLI(t, "inspect", "--format", "{{range $name, $value := .NetworkSettings.Networks}}{{$name}} {{end}}", workspace.runtime))
		if len(attached) != 1 || attached[0] != workspace.network {
			t.Fatalf("%s: attached networks = %v, want only %s", workspace.name, attached, workspace.network)
		}
		workspace.address = strings.TrimSpace(podmanCLI(t, "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", workspace.runtime))
		if net.ParseIP(workspace.address) == nil {
			t.Fatalf("%s: container address = %q", workspace.name, workspace.address)
		}
		if body := waitForLoopbackService(t, workspace.hostPort); body != workspace.id {
			t.Fatalf("%s: loopback desktop port answered %q, want %q", workspace.name, body, workspace.id)
		}
		// A reachable own service proves the probe client works, so the
		// blocked cross-workspace request below is not a false pass.
		podmanCLI(t, "exec", workspace.runtime, "/opt/probe/probe", "get", "http://127.0.0.1:5901/")
	}
	for _, from := range workspaces {
		for _, to := range workspaces {
			if from == to {
				continue
			}
			if output, err := exec.Command("podman", "exec", from.runtime, "/opt/probe/probe", "get", "http://"+to.address+":5901/").CombinedOutput(); err == nil {
				t.Fatalf("%s reached the %s workspace at %s across isolated networks", from.name, to.name, to.address)
			} else if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
				t.Fatalf("probe from %s failed to run: %v\n%s", from.name, err, output)
			}
		}
	}
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func waitForLoopbackService(t *testing.T, port int) string {
	t.Helper()
	client := http.Client{Timeout: time.Second}
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
	deadline := time.Now().Add(15 * time.Second)
	for {
		response, err := client.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr == nil {
				return string(body)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("loopback service %s did not answer: %v", url, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
