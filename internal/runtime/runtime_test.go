package runtime

import (
	"context"
	"testing"
	"time"
)

type contractRuntime struct{}

func (contractRuntime) Name(context.Context) (string, error) { return RuntimeNameDocker, nil }
func (contractRuntime) Capabilities(context.Context) (Capabilities, error) {
	return Capabilities{RuntimeName: RuntimeNameDocker, SupportsManagedLabels: true}, nil
}
func (contractRuntime) ListManaged(context.Context) ([]ObservedWorkspace, error) { return nil, nil }
func (contractRuntime) CreateWorkspace(context.Context, WorkspaceSpec) (WorkspaceHandle, error) {
	return WorkspaceHandle{}, nil
}
func (contractRuntime) StartWorkspace(context.Context, string) error               { return nil }
func (contractRuntime) StopWorkspace(context.Context, string, time.Duration) error { return nil }
func (contractRuntime) RemoveWorkspace(context.Context, string) error              { return nil }
func (contractRuntime) InspectWorkspace(context.Context, string) (ObservedWorkspace, error) {
	return ObservedWorkspace{}, nil
}

var _ Runtime = contractRuntime{}

func TestManagedLabels(t *testing.T) {
	labels := ManagedLabels("workspace-123")
	if labels[ManagedLabel] != "true" || labels[WorkspaceIDLabel] != "workspace-123" {
		t.Fatalf("unexpected managed labels: %#v", labels)
	}
}
