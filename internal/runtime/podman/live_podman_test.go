//go:build podman_integration

// Live checks against a real rootless Podman service. Unit tests can only
// assert the request COWS builds; Libpod silently ignores fields it does not
// apply, so identity, mount, and passwd behavior must be confirmed here.
//
//	COWS_PODMAN_TEST_IMAGE=docker.io/library/debian:bookworm \
//	  go test -tags podman_integration ./internal/runtime/podman/
//
// The image must already be present locally, provide sh, sleep, cat, id, and
// stat, and must not already define UID 2000. Test data lives in a temporary
// directory outside the repository.
package podman

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/runtime"
)

func TestLivePodmanConfiguredUserIdentity(t *testing.T) {
	ctx, adapter, image, dataRoot := livePodman(t)
	workspaceID := liveWorkspaceID(t)
	dataDir := filepath.Join(dataRoot, "cows-"+workspaceID, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("create directory mount: %v", err)
	}
	volumeName := "cows-" + workspaceID + "-cache"
	// Podman adds the passwd entry only when the image lacks the UID, so the
	// test uses an identity that ordinary base images do not define.
	const uid, gid = 2000, 2000
	const passwdEntry = "alice:x:2000:2000:Alice:/home/alice:/bin/sh"
	// No managed labels: a running COWS instance must not adopt this container.
	handle, err := adapter.CreateWorkspace(ctx, runtime.WorkspaceSpec{
		WorkspaceID: workspaceID,
		Image:       runtime.Image{Reference: image},
		Limits:      runtime.ResourceLimits{CPUMillis: 500, MemoryBytes: 256 << 20},
		Command:     []string{"sleep", "300"},
		User:        &runtime.ContainerUser{Username: "alice", UID: uid, GID: gid, Name: "Alice", Home: "/home/alice", Shell: "/bin/sh", PasswdEntry: passwdEntry},
		Mounts: []runtime.Mount{
			{Name: "data", Type: "bind", Source: dataDir, ContainerPath: "/data", RemapOwnership: true},
			{Name: "cache", Type: "volume", Source: volumeName, ContainerPath: "/cache", RemapOwnership: true},
		},
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("podman", "rm", "--force", "--time", "0", handle.RuntimeID).Run()
		_ = exec.Command("podman", "volume", "rm", "--force", volumeName).Run()
	})
	if err := adapter.StartWorkspace(ctx, handle.RuntimeID); err != nil {
		t.Fatalf("start workspace: %v", err)
	}

	pid := strings.TrimSpace(podmanCLI(t, "inspect", "--format", "{{.State.Pid}}", handle.RuntimeID))
	mappings, err := adapter.podmanIDMappings(ctx)
	if err != nil {
		t.Fatalf("read rootless mappings: %v", err)
	}
	for _, check := range []struct {
		kind        string
		file        string
		account     int
		containerID int64
		rootless    []idMapping
	}{
		{kind: "UID", file: "uid_map", account: os.Getuid(), containerID: uid, rootless: mappings.UIDMap},
		{kind: "GID", file: "gid_map", account: os.Getgid(), containerID: gid, rootless: mappings.GIDMap},
	} {
		ranges := readIDMap(t, filepath.Join("/proc", pid, check.file))
		for _, mapping := range ranges {
			if int64(check.account) >= mapping.HostID && int64(check.account)-mapping.HostID < mapping.Size {
				t.Fatalf("container %s map %+v includes the COWS host account %d", check.kind, ranges, check.account)
			}
		}
		want := wantHostID(t, check.rootless, check.containerID)
		if got := hostIDFor(ranges, check.containerID); got != want {
			t.Fatalf("container %s %d maps to host %d, want %d", check.kind, check.containerID, got, want)
		}
	}

	if passwd := podmanCLI(t, "exec", handle.RuntimeID, "cat", "/etc/passwd"); !containsLine(passwd, passwdEntry) {
		t.Fatalf("container /etc/passwd lacks %q:\n%s", passwdEntry, passwd)
	}
	if identity := strings.TrimSpace(podmanCLI(t, "exec", handle.RuntimeID, "id", "-u", "-n")); identity != "alice" {
		t.Fatalf("container process user = %q, want alice", identity)
	}

	podmanCLI(t, "exec", handle.RuntimeID, "sh", "-c", "echo container > /data/from-container && echo container > /cache/from-container")
	// The mapped mount is closed to the host account itself, so ownership is
	// read from the rootless namespace the file helper also uses.
	wantOwner, err := explicitNamespaceID(mappings.UIDMap, uid)
	if err != nil {
		t.Fatalf("translate container UID: %v", err)
	}
	if owner := strings.TrimSpace(podmanCLI(t, "unshare", "stat", "-c", "%u", filepath.Join(dataDir, "from-container"))); owner != strconv.FormatInt(wantOwner, 10) {
		t.Fatalf("container-written file namespace owner = %s, want %d", owner, wantOwner)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "from-container")); !os.IsPermission(err) {
		t.Fatalf("host account stat of mapped mount content error = %v, want permission denied", err)
	}

	for _, mount := range []struct {
		spec runtime.FileAccessSpec
		path string
	}{
		{spec: runtime.FileAccessSpec{MountType: "directory", Source: dataDir}, path: "/data/from-helper"},
		{spec: runtime.FileAccessSpec{MountType: "volume", Source: volumeName}, path: "/cache/from-helper"},
	} {
		spec := mount.spec
		spec.RuntimeID, spec.ContainerUID, spec.ContainerGID, spec.ConfiguredUser = handle.RuntimeID, uid, gid, true
		access, err := adapter.OpenFileAccess(ctx, spec)
		if err != nil {
			t.Fatalf("open %s file access: %v", spec.MountType, err)
		}
		if err := access.Upload(ctx, ".", "from-helper", strings.NewReader("helper\n")); err != nil {
			t.Fatalf("upload through %s file helper: %v", spec.MountType, err)
		}
		_ = access.Close()
		if owner := strings.TrimSpace(podmanCLI(t, "exec", handle.RuntimeID, "stat", "-c", "%u:%g", mount.path)); owner != fmt.Sprintf("%d:%d", uid, gid) {
			t.Fatalf("helper-uploaded %s is owned by %s inside the container, want %d:%d", mount.path, owner, uid, gid)
		}
		podmanCLI(t, "exec", handle.RuntimeID, "sh", "-c", "echo appended >> "+mount.path)
	}
}

// livePodman connects to the rootless Podman socket and prepares a temporary
// data root holding a freshly built COWS file helper.
func livePodman(t *testing.T) (context.Context, *Adapter, string, string) {
	t.Helper()
	image := os.Getenv("COWS_PODMAN_TEST_IMAGE")
	if image == "" {
		t.Skip("set COWS_PODMAN_TEST_IMAGE to a locally available image")
	}
	socket := os.Getenv("COWS_PODMAN_SOCKET")
	if socket == "" {
		socket = filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "podman", "podman.sock")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	adapter, err := New(socket)
	if err != nil {
		t.Fatalf("connect to Podman: %v", err)
	}
	dataRoot, err := os.MkdirTemp("", "cows-podman-live-")
	if err != nil {
		t.Fatalf("create live test directory: %v", err)
	}
	// Mapped files belong to subordinate IDs, so only the Podman namespace
	// can remove them.
	t.Cleanup(func() { _ = exec.Command("podman", "unshare", "rm", "-rf", dataRoot).Run() })
	adapter.helperExecutable = filepath.Join(dataRoot, "cows")
	if output, err := exec.Command("go", "build", "-o", adapter.helperExecutable, "github.com/cows-project/cows/cmd/cows").CombinedOutput(); err != nil {
		t.Fatalf("build COWS file helper: %v\n%s", err, output)
	}
	return ctx, adapter, image, dataRoot
}

func liveWorkspaceID(t *testing.T) string {
	t.Helper()
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("generate workspace ID: %v", err)
	}
	return "live-" + hex.EncodeToString(suffix)
}

func podmanCLI(t *testing.T, args ...string) string {
	t.Helper()
	output, err := exec.Command("podman", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("podman %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

// readIDMap parses /proc/<pid>/{uid,gid}_map as seen from the host, where
// HostID is the host ID outside every user namespace.
func readIDMap(t *testing.T, path string) []idMapping {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	var mappings []idMapping
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var mapping idMapping
		if _, err := fmt.Sscan(scanner.Text(), &mapping.ContainerID, &mapping.HostID, &mapping.Size); err != nil {
			t.Fatalf("parse %s line %q: %v", path, scanner.Text(), err)
		}
		mappings = append(mappings, mapping)
	}
	return mappings
}

func hostIDFor(mappings []idMapping, id int64) int64 {
	for _, mapping := range mappings {
		if id >= mapping.ContainerID && id-mapping.ContainerID < mapping.Size {
			return mapping.HostID + id - mapping.ContainerID
		}
	}
	return -1
}

// wantHostID derives the host ID of a configured container identity from the
// explicit mapping and Podman's own rootless namespace mapping.
func wantHostID(t *testing.T, rootless []idMapping, containerID int64) int64 {
	t.Helper()
	namespaceID, err := explicitNamespaceID(rootless, containerID)
	if err != nil {
		t.Fatalf("translate container ID %d: %v", containerID, err)
	}
	hostID := hostIDFor(rootless, namespaceID)
	if hostID < 0 {
		t.Fatalf("namespace ID %d is outside the rootless mapping %+v", namespaceID, rootless)
	}
	return hostID
}

func containsLine(text, line string) bool {
	for _, candidate := range strings.Split(text, "\n") {
		if strings.TrimSpace(candidate) == line {
			return true
		}
	}
	return false
}
