package runtime

import (
	"context"
	"encoding/json"
	"errors"
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

func TestFileEntryJSONPreservesDirectoryMetadata(t *testing.T) {
	encoded, err := json.Marshal(FileEntry{Name: "project", IsDir: true, Size: 4096})
	if err != nil {
		t.Fatalf("marshal file entry: %v", err)
	}
	var decoded FileEntry
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal file entry: %v", err)
	}
	if !decoded.IsDir || decoded.Name != "project" || decoded.Size != 4096 {
		t.Fatalf("decoded file entry = %+v", decoded)
	}
}

type inspectionRuntime struct {
	workspaces []ObservedWorkspace
	name       string
}

func (r inspectionRuntime) Name(context.Context) (string, error) { return r.name, nil }
func (r inspectionRuntime) Capabilities(context.Context) (Capabilities, error) {
	return Capabilities{RuntimeName: r.name, SupportsManagedLabels: true}, nil
}
func (r inspectionRuntime) ListManaged(context.Context) ([]ObservedWorkspace, error) {
	return append([]ObservedWorkspace(nil), r.workspaces...), nil
}
func (r inspectionRuntime) CreateWorkspace(context.Context, WorkspaceSpec) (WorkspaceHandle, error) {
	return WorkspaceHandle{}, ErrNotSupported
}
func (r inspectionRuntime) StartWorkspace(context.Context, string) error { return ErrNotSupported }
func (r inspectionRuntime) StopWorkspace(context.Context, string, time.Duration) error {
	return ErrNotSupported
}
func (r inspectionRuntime) RemoveWorkspace(context.Context, string) error { return ErrNotSupported }
func (r inspectionRuntime) InspectWorkspace(context.Context, string) (ObservedWorkspace, error) {
	return ObservedWorkspace{}, ErrNotSupported
}

func TestInspectSortsAndValidatesManagedWorkspaces(t *testing.T) {
	inspection, err := Inspect(context.Background(), inspectionRuntime{name: RuntimeNamePodman, workspaces: []ObservedWorkspace{
		{RuntimeID: "runtime-b", WorkspaceID: "workspace-b", State: StateStopped},
		{RuntimeID: "runtime-a", WorkspaceID: "workspace-a", State: StateRunning},
	}})
	if err != nil {
		t.Fatalf("inspect runtime: %v", err)
	}
	if inspection.RuntimeName != RuntimeNamePodman || len(inspection.Workspaces) != 2 {
		t.Fatalf("unexpected inspection: %+v", inspection)
	}
	if inspection.Workspaces[0].WorkspaceID != "workspace-a" || inspection.Workspaces[1].WorkspaceID != "workspace-b" {
		t.Fatalf("workspaces were not sorted: %+v", inspection.Workspaces)
	}
	if inspection.ObservedAt.IsZero() {
		t.Fatal("inspection timestamp is missing")
	}
}

func TestInspectRejectsDuplicateIdentity(t *testing.T) {
	_, err := Inspect(context.Background(), inspectionRuntime{workspaces: []ObservedWorkspace{
		{RuntimeID: "runtime-a", WorkspaceID: "workspace-a"},
		{RuntimeID: "runtime-b", WorkspaceID: "workspace-a"},
	}})
	if !errors.Is(err, ErrInvalidObservation) {
		t.Fatalf("duplicate observation error = %v", err)
	}
}

func TestInspectRejectsNilAdapter(t *testing.T) {
	_, err := Inspect(context.Background(), nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil adapter error = %v", err)
	}
}
