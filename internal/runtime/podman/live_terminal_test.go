//go:build podman_integration

package podman

import (
	"context"
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

func TestLivePodmanTerminalCleanupIgnoresWritableShellAndStartup(t *testing.T) {
	ctx, adapter, image, dataRoot := livePodman(t)
	workspaceID := liveWorkspaceID(t)
	dataDir := filepath.Join(dataRoot, "cows-"+workspaceID, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Both files are writable by the terminal identity. Executing either as
	// container root leaves evidence independently of terminal output.
	marker := `if [ "$(/usr/bin/id -u)" = 0 ]; then echo root >> /data/root-executed; fi
`
	for name, content := range map[string]string{
		"shell":   "#!/bin/sh\n" + marker + "exec /bin/sh \"$@\"\n",
		"startup": marker,
	} {
		if err := os.WriteFile(filepath.Join(dataDir, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	handle, err := adapter.CreateWorkspace(ctx, runtime.WorkspaceSpec{
		WorkspaceID: workspaceID,
		Image:       runtime.Image{Reference: image},
		Limits:      runtime.ResourceLimits{CPUMillis: 500, MemoryBytes: 256 << 20},
		Command:     []string{"sleep", "300"},
		User:        &runtime.ContainerUser{Username: "alice", UID: 2000, GID: 2000, Name: "Alice", Home: "/data", Shell: "/data/shell", PasswdEntry: "alice:x:2000:2000:Alice:/data:/data/shell"},
		Environment: []runtime.EnvironmentVariable{{Name: "ENV", Value: "/data/startup"}, {Name: "BASH_ENV", Value: "/data/startup"}, {Name: "LD_PRELOAD", Value: "/data/absent-library.so"}},
		Mounts:      []runtime.Mount{{Name: "data", Type: "bind", Source: dataDir, ContainerPath: "/data", RemapOwnership: true}},
	})
	if err != nil {
		t.Fatalf("create terminal workspace: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("podman", "rm", "--force", "--time", "0", handle.RuntimeID).Run() })
	if err := adapter.StartWorkspace(ctx, handle.RuntimeID); err != nil {
		t.Fatal(err)
	}
	podmanCLI(t, "exec", handle.RuntimeID, "/bin/sh", "-c", "test -x /bin/kill && test -w /data/shell && test -w /data/startup")
	terminal, err := adapter.OpenShellAs(ctx, handle.RuntimeID, 2000, []string{"/data/shell", "-l"})
	if err != nil {
		t.Fatalf("open writable shell: %v", err)
	}
	t.Cleanup(func() { _ = terminal.Close() })
	// Disable job control so the background child remains in the exec group.
	// Independently created process groups are outside disconnect cleanup.
	if _, err := terminal.Write([]byte("set +m; trap '' TERM; (trap '' TERM; sleep 300) & echo $! > /data/child.pid; echo $$ > /data/shell.pid\n")); err != nil {
		t.Fatal(err)
	}
	var pids []int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		output, err := exec.CommandContext(ctx, "podman", "unshare", "cat", filepath.Join(dataDir, "shell.pid"), filepath.Join(dataDir, "child.pid")).Output()
		if err == nil && len(strings.Fields(string(output))) == 2 {
			for _, value := range strings.Fields(string(output)) {
				pid, err := strconv.Atoi(value)
				if err != nil || pid <= 1 {
					t.Fatalf("invalid terminal test PID %q", value)
				}
				pids = append(pids, pid)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(pids) != 2 {
		t.Fatal("interactive terminal did not start its shell and background child")
	}
	if err := terminal.Close(); err != nil {
		t.Fatalf("disconnect cleanup: %v", err)
	}
	for _, pid := range pids {
		// A minimal container init may leave a killed orphan as a zombie.
		check := fmt.Sprintf("test ! -e /proc/%d/status || grep -q '^State:.*Z' /proc/%d/status", pid, pid)
		deadline := time.Now().Add(3 * time.Second)
		for {
			if err := exec.CommandContext(ctx, "podman", "exec", handle.RuntimeID, "/bin/sh", "-c", check).Run(); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("terminal process %d is still running after cleanup", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	podmanCLI(t, "exec", handle.RuntimeID, "/bin/sh", "-c", "test ! -e /data/root-executed")
	// Ensure the adapter remains usable after terminal cleanup.
	if _, err := adapter.InspectWorkspace(context.Background(), handle.RuntimeID); err != nil {
		t.Fatal(err)
	}
}
