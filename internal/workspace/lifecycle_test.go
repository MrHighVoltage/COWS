package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
	"github.com/cows-project/cows/internal/runtime"
)

type lifecycleRuntime struct {
	created                      int
	started                      int
	stopped                      int
	removed                      int
	removeErr                    error
	lastID                       string
	lastSpec                     runtime.WorkspaceSpec
	observed                     []runtime.ObservedWorkspace
	internalServiceRuntimeID     string
	internalServiceContainerPort int
	internalServiceHostPort      int
}

type desktopStream struct{ bytes.Buffer }

func (desktopStream) Close() error { return nil }

func (r *lifecycleRuntime) Name(context.Context) (string, error) {
	return runtime.RuntimeNamePodman, nil
}
func (r *lifecycleRuntime) Capabilities(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{RuntimeName: runtime.RuntimeNamePodman, SupportsResourceLimits: true, SupportsManagedLabels: true}, nil
}
func (r *lifecycleRuntime) ListManaged(context.Context) ([]runtime.ObservedWorkspace, error) {
	return append([]runtime.ObservedWorkspace(nil), r.observed...), nil
}
func (r *lifecycleRuntime) CreateWorkspace(_ context.Context, spec runtime.WorkspaceSpec) (runtime.WorkspaceHandle, error) {
	r.created++
	r.lastID = "runtime-123"
	r.lastSpec = spec
	return runtime.WorkspaceHandle{RuntimeID: r.lastID, WorkspaceID: spec.WorkspaceID}, nil
}
func (r *lifecycleRuntime) StartWorkspace(context.Context, string) error { r.started++; return nil }
func (r *lifecycleRuntime) StopWorkspace(context.Context, string, time.Duration) error {
	r.stopped++
	return nil
}
func (r *lifecycleRuntime) RemoveWorkspace(context.Context, string) error {
	r.removed++
	return r.removeErr
}
func (r *lifecycleRuntime) InspectWorkspace(context.Context, string) (runtime.ObservedWorkspace, error) {
	return runtime.ObservedWorkspace{}, nil
}
func (r *lifecycleRuntime) OpenInternalService(_ context.Context, runtimeID string, containerPort, hostPort int) (io.ReadWriteCloser, error) {
	r.internalServiceRuntimeID = runtimeID
	r.internalServiceContainerPort = containerPort
	r.internalServiceHostPort = hostPort
	return &desktopStream{}, nil
}

func TestLifecycleTimeoutStopsAndDeletesWorkspace(t *testing.T) {
	base := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	service, authService, adminID, store := testService(t)
	input := validTemplateInput()
	input.InitialConnectionTimeoutSeconds = 1
	input.StoppedRetentionSeconds = 1
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
	fake.removeErr = runtime.ErrNotFound
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
	if updated.ContainerDeletedAt.IsZero() || !updated.DataArchiveEligibleAt.IsZero() {
		t.Fatalf("timeout timestamps were not persisted as expected: %+v", updated)
	}
	if updated.Operation != "timeout-delete" || updated.OperationStatus != "succeeded" {
		t.Fatalf("delete operation status = %q/%q", updated.Operation, updated.OperationStatus)
	}
}

func TestSuccessfulRestartRequiresANewConnection(t *testing.T) {
	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	service, authService, adminID, store := testService(t)
	input := validTemplateInput()
	input.InitialConnectionTimeoutSeconds = 60
	template, err := service.CreateTemplate(context.Background(), adminID, input)
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	if _, err := authService.CreateUser(context.Background(), adminID, auth.CreateUserInput{Username: "restart-user", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	user, _, err := authService.Authenticate(context.Background(), "restart-user", "another correct password")
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
	}
	if err := authService.ChangePassword(context.Background(), user.ID, "another correct password", "changed restart password"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	value, err := service.CreateWorkspace(context.Background(), user.ID, CreateWorkspaceInput{Name: "Restart workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	fake := &lifecycleRuntime{}
	service = NewWithRuntime(store, fake)
	service.now = func() time.Time { return base }
	if err := service.StartWorkspace(context.Background(), user.ID, value.ID); err != nil {
		t.Fatalf("start workspace: %v", err)
	}
	service.now = func() time.Time { return base.Add(10 * time.Second) }
	if err := service.RecordWorkspaceConnection(context.Background(), user.ID, value.ID); err != nil {
		t.Fatalf("record connection: %v", err)
	}
	if err := service.StopWorkspace(context.Background(), user.ID, value.ID); err != nil {
		t.Fatalf("stop workspace: %v", err)
	}
	service.now = func() time.Time { return base.Add(20 * time.Second) }
	if err := service.StartWorkspace(context.Background(), user.ID, value.ID); err != nil {
		t.Fatalf("restart workspace: %v", err)
	}

	restarted, err := store.FindWorkspaceByID(context.Background(), value.ID)
	if err != nil {
		t.Fatalf("load restarted workspace: %v", err)
	}
	if !restarted.LastConnectedAt.IsZero() {
		t.Fatalf("last connection = %v, want cleared after restart", restarted.LastConnectedAt)
	}
	status := EvaluateTimeouts(restarted, base.Add(81*time.Second))
	if status.Action != TimeoutActionStop || !status.Due {
		t.Fatalf("restart timeout = %+v, want due stop", status)
	}
}

func TestManualDeleteRemovesWorkspaceRecordAfterContainer(t *testing.T) {
	service, authService, adminID, store := testService(t)
	mountRoot := t.TempDir()
	service = NewWithRuntimeAndMountRoot(store, nil, mountRoot)
	templateInput := validTemplateInput()
	templateInput.Configuration.Mounts = []domain.TemplateMount{
		{Name: "designs", Type: domain.TemplateMountDirectory, ContainerPath: "/designs"},
		{Name: "cache", Type: domain.TemplateMountVolume, ContainerPath: "/cache", ReadOnly: true},
	}
	template, err := service.CreateTemplate(context.Background(), adminID, templateInput)
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	if _, err := authService.CreateUser(context.Background(), adminID, auth.CreateUserInput{Username: "delete-user", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	user, _, err := authService.Authenticate(context.Background(), "delete-user", "another correct password")
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
	}
	if err := authService.ChangePassword(context.Background(), user.ID, "another correct password", "changed delete password"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	value, err := service.CreateWorkspace(context.Background(), user.ID, CreateWorkspaceInput{Name: "Delete me", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	fake := &lifecycleRuntime{}
	service = NewWithRuntimeAndMountRoot(store, fake, mountRoot)
	if err := service.StartWorkspace(context.Background(), user.ID, value.ID); err != nil {
		t.Fatalf("start workspace: %v", err)
	}
	if err := service.StopWorkspace(context.Background(), user.ID, value.ID); err != nil {
		t.Fatalf("stop workspace: %v", err)
	}
	if err := service.DeleteWorkspace(context.Background(), user.ID, value.ID); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mountRoot+"-archive", managedContainerName(value.ID), "designs")); err != nil {
		t.Fatalf("archived mount directory: %v", err)
	}
	activity, err := os.ReadFile(filepath.Join(mountRoot+"-archive", "archive-activity.jsonl"))
	if err != nil || !strings.Contains(string(activity), value.ID) || !strings.Contains(string(activity), "runtime-123") {
		t.Fatalf("archive activity log = %q, error = %v", activity, err)
	}
	if fake.removed != 1 {
		t.Fatalf("runtime remove calls = %d, want 1", fake.removed)
	}
	if _, err := store.FindWorkspaceByID(context.Background(), value.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("workspace lookup after delete = %v, want not found", err)
	}
	retained, err := store.ListRetainedWorkspaceVolumes(context.Background(), value.ID)
	if err != nil {
		t.Fatalf("list retained volume metadata: %v", err)
	}
	if len(retained) != 1 || retained[0].VolumeName != managedVolumeName(value.ID, templateInput.Configuration.Mounts[1]) || retained[0].OwnerUserID != user.ID || retained[0].TemplateID != template.ID || retained[0].MountName != "cache" || retained[0].ContainerPath != "/cache" || !retained[0].ReadOnly || retained[0].RetainedAt.IsZero() {
		t.Fatalf("retained volume metadata = %+v", retained)
	}
	allocations, err := store.WorkspaceAllocations(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("workspace allocations after delete: %v", err)
	}
	if allocations.WorkspaceCount != 0 || allocations.Resources.CPUMillis != 0 || allocations.Resources.MemoryBytes != 0 || allocations.Resources.StorageBytes != 0 {
		t.Fatalf("allocations after delete = %+v, want zero", allocations)
	}
}

func TestDisabledUserWorkspaceCleanupStopsArchivesAndPermitsDeletion(t *testing.T) {
	service, authService, adminID, store := testService(t)
	fake := &lifecycleRuntime{}
	mountRoot := t.TempDir()
	service = NewWithRuntimeAndMountRoot(store, fake, mountRoot)
	templateInput := validTemplateInput()
	templateInput.Configuration.Mounts = []domain.TemplateMount{{Name: "designs", Type: domain.TemplateMountDirectory, ContainerPath: "/designs"}}
	template, err := service.CreateTemplate(context.Background(), adminID, templateInput)
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	if _, err := authService.CreateUser(context.Background(), adminID, auth.CreateUserInput{Username: "cleanup-user", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	user, _, err := authService.Authenticate(context.Background(), "cleanup-user", "another correct password")
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
	}
	if err := authService.ChangePassword(context.Background(), user.ID, "another correct password", "changed cleanup password"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	value, err := service.CreateWorkspace(context.Background(), user.ID, CreateWorkspaceInput{Name: "Cleanup workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := service.StartWorkspace(context.Background(), user.ID, value.ID); err != nil {
		t.Fatalf("start workspace: %v", err)
	}
	fake.observed = []runtime.ObservedWorkspace{{RuntimeID: fake.lastID, WorkspaceID: value.ID, State: runtime.StateRunning, ObservedAt: time.Now().UTC()}}
	if err := authService.SetUserDisabled(context.Background(), adminID, user.ID, true); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	if err := service.DeleteUserWorkspaces(context.Background(), adminID, user.ID); err != nil {
		t.Fatalf("delete user workspaces: %v", err)
	}
	if fake.stopped != 1 || fake.removed != 1 {
		t.Fatalf("runtime cleanup calls = stop %d remove %d", fake.stopped, fake.removed)
	}
	if _, err := os.Stat(filepath.Join(mountRoot+"-archive", managedContainerName(value.ID), "designs")); err != nil {
		t.Fatalf("archived directory: %v", err)
	}
	if err := authService.DeleteUser(context.Background(), adminID, user.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if _, err := store.FindUserByID(context.Background(), user.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("deleted user lookup = %v", err)
	}
}

func TestOpenDesktopUsesApprovedAllocatedService(t *testing.T) {
	service, authService, adminID, store := testService(t)
	input := validTemplateInput()
	input.Configuration = domain.TemplateConfiguration{
		Secrets:  []domain.TemplateSecret{{Name: "vnc_password", Generate: true, Length: 8}},
		Services: []domain.TemplateService{{Name: "desktop", Protocol: "tcp", ContainerPort: 5900, PortPool: "desktop", HostPortStart: 10000, HostPortEnd: 10000, PasswordSecret: "vnc_password"}},
	}
	template, err := service.CreateTemplate(context.Background(), adminID, input)
	if err != nil {
		t.Fatalf("create desktop template: %v", err)
	}
	if _, err := authService.CreateUser(context.Background(), adminID, auth.CreateUserInput{Username: "desktop-user", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	user, _, err := authService.Authenticate(context.Background(), "desktop-user", "another correct password")
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
	}
	if err := authService.ChangePassword(context.Background(), user.ID, "another correct password", "changed desktop password"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	value, err := service.CreateWorkspace(context.Background(), user.ID, CreateWorkspaceInput{Name: "Desktop workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if len(value.TemplateSecrets["vnc_password"]) != 8 {
		t.Fatalf("VNC password length = %d, want 8", len(value.TemplateSecrets["vnc_password"]))
	}
	fake := &lifecycleRuntime{}
	service = NewWithRuntime(store, fake)
	if err := service.UpdateObservedState(context.Background(), value.ID, "running", "runtime-123", "", time.Now()); err != nil {
		t.Fatalf("mark workspace running: %v", err)
	}
	connection, err := service.OpenDesktop(context.Background(), user.ID, value.ID)
	if err != nil {
		t.Fatalf("open desktop: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("close desktop: %v", err)
	}
	if fake.internalServiceRuntimeID != "runtime-123" || fake.internalServiceContainerPort != 5900 || fake.internalServiceHostPort != 10000 {
		t.Fatalf("desktop target = %q:%d -> %d", fake.internalServiceRuntimeID, fake.internalServiceContainerPort, fake.internalServiceHostPort)
	}
	credentials, err := service.GetDesktopCredentials(context.Background(), user.ID, value.ID)
	if err != nil || credentials != value.TemplateSecrets["vnc_password"] {
		t.Fatalf("desktop credentials = %q, %v; want generated password", credentials, err)
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

type failingDeleteStore struct {
	repository.Store
	failRetainingStorage bool
}

func (s *failingDeleteStore) DeleteWorkspaceRetainingStorage(ctx context.Context, id string, volumes []domain.RetainedWorkspaceVolume, directory *domain.RetainedWorkspaceDirectory) error {
	if s.failRetainingStorage {
		return errors.New("injected failure")
	}
	return s.Store.DeleteWorkspaceRetainingStorage(ctx, id, volumes, directory)
}

func TestDeleteWorkspaceSurvivesRetainingStorageFailureAndSelfHealsOnRetry(t *testing.T) {
	ctx := context.Background()
	_, authService, adminID, sqliteStore := testService(t)
	failing := &failingDeleteStore{Store: sqliteStore}
	mountRoot, archiveRoot := t.TempDir(), t.TempDir()
	fake := &lifecycleRuntime{}
	service := NewWithRuntimeAndMountRoots(failing, fake, mountRoot, archiveRoot)

	template, err := service.CreateTemplate(ctx, adminID, directoryTemplateInput("Delete Survival Template", "designs"))
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	owner := newReattachUser(t, authService, adminID, "delete-survival-owner")
	value, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{Name: "delete survival workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := sqliteStore.UpdateWorkspaceObservedState(ctx, value.ID, "stopped", "runtime-delete-survival", "", "", value.CreatedAt, value.CreatedAt); err != nil {
		t.Fatalf("set stopped state: %v", err)
	}
	mountDir := filepath.Join(mountRoot, "cows-"+value.ID, "designs")
	if err := os.WriteFile(filepath.Join(mountDir, "notes.txt"), []byte("data that must survive"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	failing.failRetainingStorage = true
	if err := service.DeleteWorkspace(ctx, owner.ID, value.ID); err == nil {
		t.Fatalf("expected DeleteWorkspace to fail when DeleteWorkspaceRetainingStorage is injected to fail")
	}

	// The workspace row must still exist (nothing rolled it back), and the
	// archived file must be sitting at the archive path (archiveMountDirectories
	// already ran and is not undone), not lost.
	if _, err := service.GetWorkspace(ctx, owner.ID, value.ID); err != nil {
		t.Fatalf("workspace row should still exist after the failed delete for a retry: %v", err)
	}
	archivedFile := filepath.Join(archiveRoot, "cows-"+value.ID, "designs", "notes.txt")
	content, err := os.ReadFile(archivedFile)
	if err != nil {
		t.Fatalf("archived file should exist after the failed delete: %v", err)
	}
	if string(content) != "data that must survive" {
		t.Fatalf("archived content = %q, want original content", content)
	}

	// Retrying must succeed and produce the expected tombstone: proves the
	// partial failure self-heals rather than wedging the workspace.
	failing.failRetainingStorage = false
	if err := service.DeleteWorkspace(ctx, owner.ID, value.ID); err != nil {
		t.Fatalf("retry delete: %v", err)
	}
	if _, err := service.GetWorkspace(ctx, owner.ID, value.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("workspace should be gone after the successful retry, err = %v", err)
	}
	directories, err := sqliteStore.ListRetainedWorkspaceDirectoriesForOwner(ctx, owner.ID)
	if err != nil || len(directories) != 1 {
		t.Fatalf("retained directories after successful retry = %d, err=%v, want 1", len(directories), err)
	}
	restoredContent, err := os.ReadFile(filepath.Join(directories[0].ArchivePath, "designs", "notes.txt"))
	if err != nil || string(restoredContent) != "data that must survive" {
		t.Fatalf("final archived content = %q, err=%v, want original content", restoredContent, err)
	}
}
