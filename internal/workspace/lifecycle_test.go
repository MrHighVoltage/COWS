package workspace

import (
	"context"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/runtime"
)

type lifecycleRuntime struct {
	created  int
	started  int
	stopped  int
	removed  int
	lastID   string
	observed []runtime.ObservedWorkspace
}

func (r *lifecycleRuntime) Name(context.Context) (string, error) {
	return runtime.RuntimeNameDocker, nil
}
func (r *lifecycleRuntime) Capabilities(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{RuntimeName: runtime.RuntimeNameDocker, SupportsResourceLimits: true, SupportsManagedLabels: true}, nil
}
func (r *lifecycleRuntime) ListManaged(context.Context) ([]runtime.ObservedWorkspace, error) {
	return append([]runtime.ObservedWorkspace(nil), r.observed...), nil
}
func (r *lifecycleRuntime) CreateWorkspace(_ context.Context, spec runtime.WorkspaceSpec) (runtime.WorkspaceHandle, error) {
	r.created++
	r.lastID = "runtime-123"
	return runtime.WorkspaceHandle{RuntimeID: r.lastID, WorkspaceID: spec.WorkspaceID}, nil
}
func (r *lifecycleRuntime) StartWorkspace(context.Context, string) error { r.started++; return nil }
func (r *lifecycleRuntime) StopWorkspace(context.Context, string, time.Duration) error {
	r.stopped++
	return nil
}
func (r *lifecycleRuntime) RemoveWorkspace(context.Context, string) error { r.removed++; return nil }
func (r *lifecycleRuntime) InspectWorkspace(context.Context, string) (runtime.ObservedWorkspace, error) {
	return runtime.ObservedWorkspace{}, nil
}

func TestLifecycleTimeoutStopsAndDeletesWorkspace(t *testing.T) {
	base := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	service, authService, adminID, store := testService(t)
	input := validTemplateInput()
	input.InitialConnectionTimeoutSeconds = 1
	input.StoppedRetentionSeconds = 1
	input.DataRetentionSeconds = 60
	template, err := service.CreateTemplate(context.Background(), adminID, input)
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	if _, err := authService.CreateUser(context.Background(), adminID, auth.CreateUserInput{Username: "timeout-user", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	user, _, err := authService.Authenticate(context.Background(), "timeout-user", "another correct password")
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
	}
	if err := authService.ChangePassword(context.Background(), user.ID, "another correct password", "changed timeout password"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	value, err := service.CreateWorkspace(context.Background(), user.ID, CreateWorkspaceInput{Name: "Timeout workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	fake := &lifecycleRuntime{}
	service = NewWithRuntime(store, fake)
	service.now = func() time.Time { return base }
	if err := service.StartWorkspace(context.Background(), user.ID, value.ID); err != nil {
		t.Fatalf("start workspace: %v", err)
	}
	if fake.created != 1 || fake.started != 1 {
		t.Fatalf("runtime start calls = created %d started %d", fake.created, fake.started)
	}
	started, err := store.FindWorkspaceByID(context.Background(), value.ID)
	if err != nil {
		t.Fatalf("load started workspace: %v", err)
	}
	if started.Operation != "start" || started.OperationStatus != "succeeded" {
		t.Fatalf("start operation status = %q/%q", started.Operation, started.OperationStatus)
	}
	service.now = func() time.Time { return base.Add(2 * time.Second) }
	if err := service.RunTimeouts(context.Background()); err != nil {
		t.Fatalf("run stop timeout: %v", err)
	}
	if fake.stopped != 1 {
		t.Fatalf("stop calls = %d, want 1", fake.stopped)
	}
	service.now = func() time.Time { return base.Add(4 * time.Second) }
	if err := service.RunTimeouts(context.Background()); err != nil {
		t.Fatalf("run delete timeout: %v", err)
	}
	if fake.removed != 1 {
		t.Fatalf("remove calls = %d, want 1", fake.removed)
	}
	updated, err := store.FindWorkspaceByID(context.Background(), value.ID)
	if err != nil {
		t.Fatalf("load updated workspace: %v", err)
	}
	if updated.ContainerDeletedAt.IsZero() || updated.DataArchiveEligibleAt.IsZero() {
		t.Fatalf("timeout timestamps were not persisted: %+v", updated)
	}
	if updated.Operation != "timeout-delete" || updated.OperationStatus != "succeeded" {
		t.Fatalf("delete operation status = %q/%q", updated.Operation, updated.OperationStatus)
	}
}

func TestReconcilePersistsManualRuntimeStop(t *testing.T) {
	base := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	service, authService, adminID, store := testService(t)
	template, err := service.CreateTemplate(context.Background(), adminID, validTemplateInput())
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	if _, err := authService.CreateUser(context.Background(), adminID, auth.CreateUserInput{Username: "reconcile-user", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	user, _, err := authService.Authenticate(context.Background(), "reconcile-user", "another correct password")
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
	}
	if err := authService.ChangePassword(context.Background(), user.ID, "another correct password", "changed reconcile password"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	value, err := service.CreateWorkspace(context.Background(), user.ID, CreateWorkspaceInput{Name: "Reconcile workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	fake := &lifecycleRuntime{}
	service = NewWithRuntime(store, fake)
	service.now = func() time.Time { return base }
	if err := service.StartWorkspace(context.Background(), user.ID, value.ID); err != nil {
		t.Fatalf("start workspace: %v", err)
	}
	fake.observed = []runtime.ObservedWorkspace{{RuntimeID: fake.lastID, WorkspaceID: value.ID, State: runtime.StateExited, ObservedAt: base.Add(time.Minute)}}
	service.now = func() time.Time { return base.Add(2 * time.Minute) }
	if err := service.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile stopped workspace: %v", err)
	}
	updated, err := store.FindWorkspaceByID(context.Background(), value.ID)
	if err != nil {
		t.Fatalf("load reconciled workspace: %v", err)
	}
	if updated.ObservedState != string(runtime.StateStopped) || updated.StoppedAt.IsZero() {
		t.Fatalf("manual stop was not persisted: %+v", updated)
	}
	fake.observed = append(fake.observed, runtime.ObservedWorkspace{RuntimeID: "orphan-runtime", WorkspaceID: "orphan-workspace", State: runtime.StateStopped, ObservedAt: base.Add(2 * time.Minute)})
	if err := service.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile orphan workspace: %v", err)
	}
	fake.observed = nil
	if err := service.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile missing workspace: %v", err)
	}
	updated, err = store.FindWorkspaceByID(context.Background(), value.ID)
	if err != nil {
		t.Fatalf("load missing workspace: %v", err)
	}
	if updated.ObservedState != "missing" {
		t.Fatalf("missing runtime was not recorded: %+v", updated)
	}
	if !updated.ContainerDeletedAt.IsZero() {
		t.Fatal("missing runtime was incorrectly marked deleted")
	}
}
