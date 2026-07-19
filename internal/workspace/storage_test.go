package workspace

import (
	"context"
	"sync"
	"testing"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/runtime"
)

type countingStorageRuntime struct {
	mu    sync.Mutex
	calls int
}

func (r *countingStorageRuntime) WorkspaceStorageUsage(context.Context, runtime.StorageUsageSpec) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return 42, nil
}

func (r *countingStorageRuntime) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestStorageUsageProviderCachesWorkspaceMeasurement(t *testing.T) {
	runtimeAdapter := &countingStorageRuntime{}
	provider := NewStorageUsageProvider(runtimeAdapter, t.TempDir())
	workspace := domain.Workspace{ID: "workspace-1", RuntimeID: "container-1", ObservedState: string(runtime.StateRunning)}

	for i := 0; i < 2; i++ {
		usage, err := provider.WorkspaceStorageUsage(context.Background(), workspace)
		if err != nil || usage != 42 {
			t.Fatalf("measurement %d = %d/%v", i, usage, err)
		}
	}
	if calls := runtimeAdapter.callCount(); calls != 1 {
		t.Fatalf("runtime measurements = %d, want 1", calls)
	}

	workspace.RuntimeID = "container-2"
	if _, err := provider.WorkspaceStorageUsage(context.Background(), workspace); err != nil {
		t.Fatalf("new runtime measurement: %v", err)
	}
	if calls := runtimeAdapter.callCount(); calls != 2 {
		t.Fatalf("runtime measurements after identity change = %d, want 2", calls)
	}
}
