package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
	"github.com/cows-project/cows/internal/runtime"
)

// volumeLifecycleRuntime adds runtime.VolumeRuntime to the existing
// lifecycleRuntime fake so reattachment's VolumeExists staleness check has
// something to call.
type volumeLifecycleRuntime struct {
	*lifecycleRuntime
	missing map[string]bool
}

func (r *volumeLifecycleRuntime) VolumeExists(_ context.Context, name string) (bool, error) {
	return !r.missing[name], nil
}
func (r *volumeLifecycleRuntime) RemoveVolume(context.Context, string) error { return nil }

func newReattachUser(t *testing.T, authService *auth.Service, adminID, username string) domain.User {
	t.Helper()
	ctx := context.Background()
	if _, err := authService.CreateUser(ctx, adminID, auth.CreateUserInput{Username: username, Password: "a correct reattach password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	user, _, err := authService.Authenticate(ctx, username, "a correct reattach password")
	if err != nil {
		t.Fatalf("authenticate %s: %v", username, err)
	}
	if err := authService.ChangePassword(ctx, user.ID, "a correct reattach password", "changed "+username+" password"); err != nil {
		t.Fatalf("change password for %s: %v", username, err)
	}
	return user
}

func directoryTemplateInput(name, mountName string) TemplateInput {
	input := validTemplateInput()
	input.Name = name
	input.AccessMethods = []domain.AccessMethod{domain.AccessFiles}
	input.Configuration.Mounts = []domain.TemplateMount{{Name: mountName, Type: domain.TemplateMountDirectory, ContainerPath: "/data", FileManager: true}}
	return input
}

func volumeTemplateInput(name, mountName string, uid int64) TemplateInput {
	input := validTemplateInput()
	input.Name = name
	input.AccessMethods = []domain.AccessMethod{domain.AccessFiles}
	input.Configuration.Mounts = []domain.TemplateMount{{Name: mountName, Type: domain.TemplateMountVolume, ContainerPath: "/data", FileManager: true}}
	uidValue, gidValue := uid, uid
	input.Configuration.ContainerUser = &domain.TemplateContainerUser{UID: &uidValue, GID: &gidValue}
	return input
}

func TestDirectoryReattachmentRestoresContentIntoNewWorkspace(t *testing.T) {
	ctx := context.Background()
	_, authService, adminID, store := testService(t)
	mountRoot := t.TempDir()
	archiveRoot := t.TempDir()
	service := NewWithRuntimeAndMountRoots(store, &lifecycleRuntime{}, mountRoot, archiveRoot)

	oldTemplate, err := service.CreateTemplate(ctx, adminID, directoryTemplateInput("Old Directory Template", "designs"))
	if err != nil {
		t.Fatalf("create old template: %v", err)
	}
	owner := newReattachUser(t, authService, adminID, "directory-owner")

	old, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{Name: "old workspace", TemplateID: oldTemplate.ID})
	if err != nil {
		t.Fatalf("create old workspace: %v", err)
	}
	oldMountDir := filepath.Join(mountRoot, "cows-"+old.ID, "designs")
	if err := os.WriteFile(filepath.Join(oldMountDir, "notes.txt"), []byte("hello from the old workspace"), 0o600); err != nil {
		t.Fatalf("seed file into old mount: %v", err)
	}

	if err := service.DeleteWorkspace(ctx, owner.ID, old.ID); err != nil {
		t.Fatalf("delete old workspace: %v", err)
	}
	directories, err := store.ListRetainedWorkspaceDirectoriesForOwner(ctx, owner.ID)
	if err != nil || len(directories) != 1 {
		t.Fatalf("retained directories = %d, err=%v, want 1", len(directories), err)
	}
	if directories[0].WorkspaceName != "old workspace" || len(directories[0].Mounts) != 1 || directories[0].Mounts[0].Name != "designs" {
		t.Fatalf("unexpected retained directory: %+v", directories[0])
	}

	newTemplate, err := service.CreateTemplate(ctx, adminID, directoryTemplateInput("New Directory Template", "designs"))
	if err != nil {
		t.Fatalf("create new template: %v", err)
	}
	next, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{Name: "new workspace", TemplateID: newTemplate.ID, ReattachDirectoriesFrom: old.ID})
	if err != nil {
		t.Fatalf("create new workspace with directory reattachment: %v", err)
	}
	restored, err := os.ReadFile(filepath.Join(mountRoot, "cows-"+next.ID, "designs", "notes.txt"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(restored) != "hello from the old workspace" {
		t.Fatalf("restored content = %q, want original content", restored)
	}
	if _, err := os.Stat(filepath.Join(archiveRoot, "cows-"+old.ID, "designs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archived 'designs' mount should have been consumed by rename, stat err = %v", err)
	}
	if directories, err := store.ListRetainedWorkspaceDirectoriesForOwner(ctx, owner.ID); err != nil || len(directories) != 0 {
		t.Fatalf("retained directories after reattachment = %d, err=%v, want 0 (consumed)", len(directories), err)
	}
}

// TestDirectoryReattachmentSurvivesLaterFailure reproduces the reported bug:
// a directory tombstone is consumed and its archived content restored into
// the new workspace's mount directory, but a later step in CreateWorkspace
// (here, port pool exhaustion) fails. ADR 0025 promises the archived data is
// never deleted by this path, only its self-service discoverability - so the
// retained directory must still be listed (and its content intact) after the
// failed creation, exactly as if the reattachment had never been attempted.
func TestDirectoryReattachmentSurvivesLaterFailure(t *testing.T) {
	ctx := context.Background()
	_, authService, adminID, store := testService(t)
	mountRoot := t.TempDir()
	archiveRoot := t.TempDir()
	service := NewWithRuntimeAndMountRoots(store, &lifecycleRuntime{}, mountRoot, archiveRoot)

	portService := domain.TemplateService{Name: "desktop", Protocol: "tcp", ContainerPort: 5901, PortPool: "vnc", HostPortStart: 21000, HostPortEnd: 21000}
	oldTemplate, err := service.CreateTemplate(ctx, adminID, directoryTemplateInput("Old Directory Template", "designs"))
	if err != nil {
		t.Fatalf("create old template: %v", err)
	}
	owner := newReattachUser(t, authService, adminID, "directory-owner-2")

	old, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{Name: "old workspace", TemplateID: oldTemplate.ID})
	if err != nil {
		t.Fatalf("create old workspace: %v", err)
	}
	oldMountDir := filepath.Join(mountRoot, "cows-"+old.ID, "designs")
	if err := os.WriteFile(filepath.Join(oldMountDir, "notes.txt"), []byte("hello from the old workspace"), 0o600); err != nil {
		t.Fatalf("seed file into old mount: %v", err)
	}
	if err := service.DeleteWorkspace(ctx, owner.ID, old.ID); err != nil {
		t.Fatalf("delete old workspace: %v", err)
	}

	newTemplateInput := directoryTemplateInput("New Directory Template", "designs")
	newTemplateInput.Configuration.Services = []domain.TemplateService{portService}
	newTemplate, err := service.CreateTemplate(ctx, adminID, newTemplateInput)
	if err != nil {
		t.Fatalf("create new template: %v", err)
	}

	// Exhaust the single-port pool so reserveWorkspacePorts fails - a step
	// that runs after the tombstone is consumed and its content restored.
	// The allocation's workspace_id has a foreign key onto workspaces(id), so
	// a real workspace row is needed to hold the blocking reservation.
	blocker, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{Name: "port blocker", TemplateID: oldTemplate.ID})
	if err != nil {
		t.Fatalf("create blocker workspace: %v", err)
	}
	if err := store.ReserveWorkspacePort(ctx, domain.PortAllocation{WorkspaceID: blocker.ID, ServiceName: "desktop", Protocol: "tcp", ContainerPort: 5901, PortPool: "vnc", HostPort: 21000}); err != nil {
		t.Fatalf("pre-reserve blocking port: %v", err)
	}

	if _, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{Name: "new workspace", TemplateID: newTemplate.ID, ReattachDirectoriesFrom: old.ID}); !errors.Is(err, ErrPortPoolUnavailable) {
		t.Fatalf("create workspace with exhausted ports error = %v, want ErrPortPoolUnavailable", err)
	}

	directories, err := store.ListRetainedWorkspaceDirectoriesForOwner(ctx, owner.ID)
	if err != nil || len(directories) != 1 {
		t.Fatalf("retained directories after failed creation = %d, err=%v, want 1 (tombstone must survive an unrelated later failure)", len(directories), err)
	}
	restored, err := os.ReadFile(filepath.Join(directories[0].ArchivePath, "designs", "notes.txt"))
	if err != nil {
		t.Fatalf("read archived file after failed creation: %v", err)
	}
	if string(restored) != "hello from the old workspace" {
		t.Fatalf("archived content = %q, want original content", restored)
	}
}

func TestVolumeReattachmentUsesExistingVolumeNameAndRemapsOwnership(t *testing.T) {
	ctx := context.Background()
	_, authService, adminID, store := testService(t)
	fake := &volumeLifecycleRuntime{lifecycleRuntime: &lifecycleRuntime{}}
	service := NewWithRuntimeAndMountRoots(store, fake, t.TempDir(), t.TempDir())

	oldTemplate, err := service.CreateTemplate(ctx, adminID, volumeTemplateInput("Old Volume Template", "data", 1000))
	if err != nil {
		t.Fatalf("create old template: %v", err)
	}
	owner := newReattachUser(t, authService, adminID, "volume-owner")
	old, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{Name: "old volume workspace", TemplateID: oldTemplate.ID})
	if err != nil {
		t.Fatalf("create old workspace: %v", err)
	}
	// Give it a runtime container so DeleteWorkspace takes the tombstone-
	// creating branch, mirroring the pattern in file_access_test.go.
	if err := store.UpdateWorkspaceObservedState(ctx, old.ID, "stopped", "runtime-abc", "", "", old.CreatedAt, old.CreatedAt); err != nil {
		t.Fatalf("set stopped state: %v", err)
	}
	if err := service.DeleteWorkspace(ctx, owner.ID, old.ID); err != nil {
		t.Fatalf("delete old workspace: %v", err)
	}
	volumes, err := store.ListRetainedWorkspaceVolumesForOwner(ctx, owner.ID)
	if err != nil || len(volumes) != 1 {
		t.Fatalf("retained volumes = %d, err=%v, want 1", len(volumes), err)
	}
	oldVolumeName := volumes[0].VolumeName

	// A different template, different container UID, same logical mount name.
	newTemplate, err := service.CreateTemplate(ctx, adminID, volumeTemplateInput("New Volume Template", "data", 2000))
	if err != nil {
		t.Fatalf("create new template: %v", err)
	}
	next, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{
		Name: "new volume workspace", TemplateID: newTemplate.ID,
		ReattachVolumesFrom: map[string]string{"data": old.ID},
	})
	if err != nil {
		t.Fatalf("create new workspace with volume reattachment: %v", err)
	}
	if fake.lastSpec.WorkspaceID != next.ID {
		t.Fatalf("runtime.CreateWorkspace was not called for the reattaching workspace")
	}
	var mount *runtime.Mount
	for i := range fake.lastSpec.Mounts {
		if fake.lastSpec.Mounts[i].Name == "data" {
			mount = &fake.lastSpec.Mounts[i]
		}
	}
	if mount == nil {
		t.Fatalf("no 'data' mount in the runtime spec: %+v", fake.lastSpec.Mounts)
	}
	if mount.Source != oldVolumeName {
		t.Fatalf("mount source = %q, want the old volume name %q (reattached)", mount.Source, oldVolumeName)
	}
	if !mount.RemapOwnership {
		t.Fatalf("reattached volume mount must set RemapOwnership so ownership is corrected for the new template's UID")
	}
	if volumes, err := store.ListRetainedWorkspaceVolumesForOwner(ctx, owner.ID); err != nil || len(volumes) != 0 {
		t.Fatalf("retained volumes after reattachment = %d, err=%v, want 0 (consumed)", len(volumes), err)
	}
}

func TestReattachmentRejectsAnotherUsersRetainedStorage(t *testing.T) {
	ctx := context.Background()
	_, authService, adminID, store := testService(t)
	mountRoot := t.TempDir()
	service := NewWithRuntimeAndMountRoots(store, &lifecycleRuntime{}, mountRoot, t.TempDir())

	template, err := service.CreateTemplate(ctx, adminID, directoryTemplateInput("Cross User Template", "designs"))
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	owner := newReattachUser(t, authService, adminID, "storage-owner")
	stranger := newReattachUser(t, authService, adminID, "storage-stranger")

	old, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{Name: "owner workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create owner workspace: %v", err)
	}
	if err := service.DeleteWorkspace(ctx, owner.ID, old.ID); err != nil {
		t.Fatalf("delete owner workspace: %v", err)
	}

	if _, err := service.CreateWorkspace(ctx, stranger.ID, CreateWorkspaceInput{
		Name: "stranger workspace", TemplateID: template.ID, ReattachDirectoriesFrom: old.ID,
	}); !errors.Is(err, ErrRetainedStorageIncompatible) {
		t.Fatalf("cross-user reattachment error = %v, want ErrRetainedStorageIncompatible", err)
	}
	// The tombstone must still belong to its real owner: rejecting the
	// stranger's request must not have consumed it.
	if directories, err := store.ListRetainedWorkspaceDirectoriesForOwner(ctx, owner.ID); err != nil || len(directories) != 1 {
		t.Fatalf("owner's retained directories after rejected cross-user attempt = %d, err=%v, want 1 (untouched)", len(directories), err)
	}
}

func TestReattachmentRejectsIncompatibleMount(t *testing.T) {
	ctx := context.Background()
	_, authService, adminID, store := testService(t)
	service := NewWithRuntimeAndMountRoots(store, &lifecycleRuntime{}, t.TempDir(), t.TempDir())

	directoryTemplate, err := service.CreateTemplate(ctx, adminID, directoryTemplateInput("Directory Type Template", "designs"))
	if err != nil {
		t.Fatalf("create directory template: %v", err)
	}
	// A template whose only file_manager mount is a volume named "designs" -
	// same name, wrong type.
	mismatchedTemplate, err := service.CreateTemplate(ctx, adminID, volumeTemplateInput("Mismatched Volume Template", "designs", 1000))
	if err != nil {
		t.Fatalf("create mismatched template: %v", err)
	}
	owner := newReattachUser(t, authService, adminID, "mismatch-owner")

	old, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{Name: "directory workspace", TemplateID: directoryTemplate.ID})
	if err != nil {
		t.Fatalf("create directory workspace: %v", err)
	}
	if err := service.DeleteWorkspace(ctx, owner.ID, old.ID); err != nil {
		t.Fatalf("delete directory workspace: %v", err)
	}
	if _, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{
		Name: "mismatched workspace", TemplateID: mismatchedTemplate.ID,
		ReattachVolumesFrom: map[string]string{"designs": old.ID},
	}); !errors.Is(err, ErrRetainedStorageIncompatible) {
		t.Fatalf("mount-type-mismatched reattachment error = %v, want ErrRetainedStorageIncompatible", err)
	}
}

func TestReattachmentFailsClosedWhenVolumeNoLongerExists(t *testing.T) {
	ctx := context.Background()
	_, authService, adminID, store := testService(t)
	fake := &volumeLifecycleRuntime{lifecycleRuntime: &lifecycleRuntime{}, missing: map[string]bool{}}
	service := NewWithRuntimeAndMountRoots(store, fake, t.TempDir(), t.TempDir())

	template, err := service.CreateTemplate(ctx, adminID, volumeTemplateInput("Stale Volume Template", "data", 1000))
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	owner := newReattachUser(t, authService, adminID, "stale-owner")
	old, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{Name: "stale workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := store.UpdateWorkspaceObservedState(ctx, old.ID, "stopped", "runtime-stale", "", "", old.CreatedAt, old.CreatedAt); err != nil {
		t.Fatalf("set stopped state: %v", err)
	}
	if err := service.DeleteWorkspace(ctx, owner.ID, old.ID); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
	volumes, err := store.ListRetainedWorkspaceVolumesForOwner(ctx, owner.ID)
	if err != nil || len(volumes) != 1 {
		t.Fatalf("retained volumes = %d, err=%v, want 1", len(volumes), err)
	}
	fake.missing[volumes[0].VolumeName] = true

	if _, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{
		Name: "recovery attempt", TemplateID: template.ID,
		ReattachVolumesFrom: map[string]string{"data": old.ID},
	}); !errors.Is(err, ErrRetainedStorageIncompatible) {
		t.Fatalf("stale-volume reattachment error = %v, want ErrRetainedStorageIncompatible", err)
	}
	// A stale tombstone is consumed as cleanup even though the reattachment
	// itself was rejected - see consumeReattachedVolumes.
	if volumes, err := store.ListRetainedWorkspaceVolumesForOwner(ctx, owner.ID); err != nil || len(volumes) != 0 {
		t.Fatalf("retained volumes after stale rejection = %d, err=%v, want 0 (cleaned up)", len(volumes), err)
	}
}

func TestConsumeRetainedWorkspaceVolumeConflictOnDoubleConsume(t *testing.T) {
	ctx := context.Background()
	_, authService, adminID, store := testService(t)
	service := NewWithRuntimeAndMountRoots(store, &lifecycleRuntime{}, t.TempDir(), t.TempDir())
	template, err := service.CreateTemplate(ctx, adminID, volumeTemplateInput("Race Volume Template", "data", 1000))
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	owner := newReattachUser(t, authService, adminID, "race-owner")
	old, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{Name: "race workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := store.UpdateWorkspaceObservedState(ctx, old.ID, "stopped", "runtime-race", "", "", old.CreatedAt, old.CreatedAt); err != nil {
		t.Fatalf("set stopped state: %v", err)
	}
	if err := service.DeleteWorkspace(ctx, owner.ID, old.ID); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
	if _, err := store.ConsumeRetainedWorkspaceVolume(ctx, old.ID, "data", owner.ID); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if _, err := store.ConsumeRetainedWorkspaceVolume(ctx, old.ID, "data", owner.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("second consume error = %v, want ErrNotFound (already gone)", err)
	}
}

func TestRestoreDirectoryMountsRollsBackPartialMoveOnFailure(t *testing.T) {
	root := t.TempDir()
	archiveRoot := t.TempDir()
	workspaceID := "new-workspace"
	archivedContainerPath := filepath.Join(archiveRoot, "cows-old-workspace")

	mounts := []domain.TemplateMount{
		{Name: "alpha", Type: domain.TemplateMountDirectory, ContainerPath: "/alpha"},
		{Name: "beta", Type: domain.TemplateMountDirectory, ContainerPath: "/beta"},
	}
	archivedMounts := []domain.RetainedDirectoryMount{
		{Name: "alpha"},
		{Name: "beta"},
	}

	for _, name := range []string{"alpha", "beta"} {
		src := filepath.Join(archivedContainerPath, name)
		if err := os.MkdirAll(src, 0o700); err != nil {
			t.Fatalf("seed archived %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("content-"+name), 0o600); err != nil {
			t.Fatalf("seed archived %s file: %v", name, err)
		}
	}

	if err := ensureMountDirectories(root, workspaceID, mounts); err != nil {
		t.Fatalf("ensure mount directories: %v", err)
	}
	// Sabotage the SECOND mount's destination (non-empty directories fail
	// os.Remove with ENOTEMPTY regardless of user/permissions, so this is a
	// reliable, portable way to force a failure after the first move has
	// already succeeded, without relying on permission bits that root
	// ignores).
	betaDestination := filepath.Join(root, "cows-"+workspaceID, "beta")
	if err := os.WriteFile(filepath.Join(betaDestination, "blocker.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("sabotage beta destination: %v", err)
	}

	err := restoreDirectoryMounts(root, archivedContainerPath, workspaceID, mounts, archivedMounts)
	if !errors.Is(err, ErrMountUnavailable) {
		t.Fatalf("restoreDirectoryMounts error = %v, want ErrMountUnavailable", err)
	}

	// The first mount's move must have been rolled back: its archived
	// content must be back at the original archive path, not left sitting
	// inside the new workspace's mount tree where a caller's failure
	// cleanup would delete it.
	restoredAlpha := filepath.Join(root, "cows-"+workspaceID, "alpha", "file.txt")
	if _, err := os.Stat(restoredAlpha); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("alpha should have been rolled back out of the new workspace, stat err = %v", err)
	}
	archivedAlpha := filepath.Join(archivedContainerPath, "alpha", "file.txt")
	content, err := os.ReadFile(archivedAlpha)
	if err != nil {
		t.Fatalf("alpha content should be back at the archive path: %v", err)
	}
	if string(content) != "content-alpha" {
		t.Fatalf("rolled-back alpha content = %q, want original content", content)
	}
}
