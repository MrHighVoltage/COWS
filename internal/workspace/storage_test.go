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

type mountCaptureRuntime struct {
	spec runtime.StorageUsageSpec
}

func (r *mountCaptureRuntime) WorkspaceStorageUsage(_ context.Context, spec runtime.StorageUsageSpec) (int64, error) {
	r.spec = spec
	return 1, nil
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

func TestStorageUsageProviderSkipsNamedVolumesBeforeFirstStart(t *testing.T) {
	runtimeAdapter := &mountCaptureRuntime{}
	provider := NewStorageUsageProvider(runtimeAdapter, t.TempDir())
	value := domain.Workspace{
		ID: "workspace-1", ObservedState: "unknown",
		TemplateConfiguration: domain.TemplateConfiguration{Mounts: []domain.TemplateMount{
			{Name: "data", Type: domain.TemplateMountVolume, ContainerPath: "/data"},
			{Name: "home", Type: domain.TemplateMountDirectory, ContainerPath: "/home"},
		}},
	}
	if _, err := provider.WorkspaceStorageUsage(context.Background(), value); err != nil {
		t.Fatalf("measure never-started workspace: %v", err)
	}
	if len(runtimeAdapter.spec.Mounts) != 1 || runtimeAdapter.spec.Mounts[0].MountType != domain.TemplateMountDirectory {
		t.Fatalf("never-started mounts = %+v, want only the managed directory", runtimeAdapter.spec.Mounts)
	}

	// Once a container exists, the named volume exists too and must be
	// measured; skipping it unconditionally would hide retained data.
	value.RuntimeID = "container-1"
	value.ObservedState = string(runtime.StateRunning)
	if _, err := provider.WorkspaceStorageUsage(context.Background(), value); err != nil {
		t.Fatalf("measure running workspace: %v", err)
	}
	if len(runtimeAdapter.spec.Mounts) != 2 {
		t.Fatalf("running mounts = %+v, want volume and directory", runtimeAdapter.spec.Mounts)
	}
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
