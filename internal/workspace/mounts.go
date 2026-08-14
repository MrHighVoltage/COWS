package workspace

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/cows-project/cows/internal/archive"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/runtime"
)

var ErrMountUnavailable = errors.New("workspace mount is unavailable")

type FileMount struct {
	Name          string
	ContainerPath string
	Root          string
	RuntimeID     string
	MountType     string
	Source        string
	ContainerUID  int64
	ContainerGID  int64
	ReadOnly      bool
}

func (s *Service) ListFileMounts(ctx context.Context, actorID, workspaceID string) ([]FileMount, error) {
	value, err := s.GetWorkspace(ctx, actorID, workspaceID)
	if err != nil {
		return nil, err
	}
	switch value.ObservedState {
	case string(runtime.StateRunning), string(runtime.StateStopped), string(runtime.StateExited):
		// Rootless Podman file access is storage-backed and does not require a
		// process inside the workspace to be running.
	default:
		return nil, ErrFileManagerNotAvailable
	}
	template, err := s.store.FindTemplateByID(ctx, value.TemplateID)
	if err != nil {
		return nil, err
	}
	if !hasAccessMethod(template.AccessMethods, domain.AccessFiles) {
		return nil, ErrFileManagerNotAvailable
	}
	configuration, err := s.effectiveConfiguration(ctx, value)
	if err != nil {
		return nil, err
	}
	if err := ensureMountDirectories(s.mountRoot, value.ID, configuration.Mounts); err != nil {
		return nil, err
	}
	owner, err := s.store.FindUserByID(ctx, value.OwnerUserID)
	if err != nil {
		return nil, err
	}
	allocations, err := s.store.ListWorkspacePortAllocations(ctx, value.ID)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveConfiguration(configuration, owner, value.ID, value.Name, allocations, value.TemplateSecrets)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(s.mountRoot)
	if err != nil {
		return nil, ErrMountUnavailable
	}
	mounts := make([]FileMount, 0)
	for _, mount := range configuration.Mounts {
		if !mount.FileManager {
			continue
		}
		mountType := normalizedMountType(mount.Type)
		mountRoot := ""
		source := managedVolumeName(value.ID, mount)
		if mountType == domain.TemplateMountDirectory {
			mountRoot = filepath.Join(root, mountRootName(value.ID, mount))
			source = mountRoot
		}
		uid, gid := int64(0), int64(0)
		if resolved.User != nil {
			uid, gid = resolved.User.UID, resolved.User.GID
		}
		mounts = append(mounts, FileMount{Name: mount.Name, ContainerPath: mount.ContainerPath, Root: mountRoot, RuntimeID: value.RuntimeID, MountType: mountType, Source: source, ContainerUID: uid, ContainerGID: gid, ReadOnly: mount.ReadOnly})
	}
	if len(mounts) == 0 {
		return nil, ErrFileManagerNotAvailable
	}
	return mounts, nil
}

func normalizedMountType(value string) string {
	if value == "" {
		return domain.TemplateMountVolume
	}
	return value
}

func managedContainerName(workspaceID string) string {
	return "cows-" + workspaceID
}

func managedMountName(workspaceID string, mount domain.TemplateMount) string {
	return mount.NamePrefix + mount.Name + mount.NameSuffix
}

func managedVolumeName(workspaceID string, mount domain.TemplateMount) string {
	return managedContainerName(workspaceID) + "-" + managedMountName(workspaceID, mount)
}

func mountRootName(workspaceID string, mount domain.TemplateMount) string {
	return filepath.Join(managedContainerName(workspaceID), managedMountName(workspaceID, mount))
}

// materializeMount resolves one template mount definition into a runtime
// mount spec. volumeSourceOverride, when non-empty, is used as the volume
// Source instead of the deterministic managedVolumeName — this is how
// self-service reattachment (see decision 0025) attaches a retained volume
// by its exact stored name instead of the fresh name this workspace would
// otherwise compute. RemapOwnership is unconditional for volumes, matching
// directory mounts: a freshly created empty volume has nothing to chown, so
// the cost is negligible, and a reattached volume's content is always
// corrected to the new container's identity on first start.
func materializeMount(root, workspaceID string, mount domain.TemplateMount, volumeSourceOverride string) (runtime.Mount, error) {
	if mount.Name == "" {
		return runtime.Mount{}, ErrMountUnavailable
	}
	switch normalizedMountType(mount.Type) {
	case domain.TemplateMountVolume:
		source := managedVolumeName(workspaceID, mount)
		if volumeSourceOverride != "" {
			source = volumeSourceOverride
		}
		return runtime.Mount{Name: mount.Name, Type: domain.TemplateMountVolume, Source: source, ContainerPath: mount.ContainerPath, ReadOnly: mount.ReadOnly, RemapOwnership: true}, nil
	case domain.TemplateMountDirectory:
		if root == "" {
			return runtime.Mount{}, ErrMountUnavailable
		}
		path, err := filepath.Abs(filepath.Join(root, mountRootName(workspaceID, mount)))
		if err != nil {
			return runtime.Mount{}, ErrMountUnavailable
		}
		return runtime.Mount{Name: mount.Name, Type: "bind", Source: path, ContainerPath: mount.ContainerPath, ReadOnly: mount.ReadOnly, RemapOwnership: true}, nil
	default:
		return runtime.Mount{}, ErrMountUnavailable
	}
}

func ensureMountDirectories(root, workspaceID string, mounts []domain.TemplateMount) error {
	if len(mounts) == 0 {
		return nil
	}
	if root == "" {
		for _, mount := range mounts {
			if normalizedMountType(mount.Type) == domain.TemplateMountDirectory {
				return ErrMountUnavailable
			}
		}
		return nil
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return ErrMountUnavailable
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return ErrMountUnavailable
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return ErrMountUnavailable
	}
	defer rootHandle.Close()
	containerName := managedContainerName(workspaceID)
	if err := rootHandle.Mkdir(containerName, 0o711); err != nil && !errors.Is(err, os.ErrExist) {
		return ErrMountUnavailable
	}
	if err := rootHandle.Chmod(containerName, 0o711); err != nil {
		return ErrMountUnavailable
	}
	containerHandle, err := rootHandle.OpenRoot(containerName)
	if err != nil {
		return ErrMountUnavailable
	}
	defer containerHandle.Close()
	for _, mount := range mounts {
		if normalizedMountType(mount.Type) != domain.TemplateMountDirectory {
			continue
		}
		mode := os.FileMode(0o500)
		if !mount.ReadOnly {
			mode = 0o700
		}
		name := managedMountName(workspaceID, mount)
		if _, err := containerHandle.Lstat(name); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return ErrMountUnavailable
		}
		if err := containerHandle.Mkdir(name, mode); err != nil {
			return ErrMountUnavailable
		}
	}
	return nil
}

func archiveMountDirectories(root, archiveRoot, workspaceID string, mounts []domain.TemplateMount) error {
	needsDirectory := false
	for _, mount := range mounts {
		if normalizedMountType(mount.Type) == domain.TemplateMountDirectory {
			needsDirectory = true
			break
		}
	}
	if root == "" || archiveRoot == "" || !needsDirectory {
		return nil
	}
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		return ErrMountUnavailable
	}
	if err := os.Chmod(archiveRoot, 0o700); err != nil {
		return ErrMountUnavailable
	}
	sourceRoot, err := filepath.Abs(filepath.Join(root, managedContainerName(workspaceID)))
	if err != nil {
		return ErrMountUnavailable
	}
	destinationRoot, err := filepath.Abs(filepath.Join(archiveRoot, managedContainerName(workspaceID)))
	if err != nil {
		return ErrMountUnavailable
	}
	if _, err := os.Lstat(sourceRoot); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return ErrMountUnavailable
	}
	if _, err := os.Lstat(destinationRoot); err == nil {
		return ErrMountUnavailable
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrMountUnavailable
	}
	if err := os.Rename(sourceRoot, destinationRoot); err != nil {
		// Rename is intentionally used instead of a copy: archive moves must be
		// atomic and must not duplicate an unbounded workspace directory.
		return ErrMountUnavailable
	}
	return nil
}

// restoreDirectoryMounts renames each archived directory-type mount from a
// retained-directory tombstone into a newly created workspace's
// corresponding mount directory (self-service reattachment, decision 0025).
// It is all-or-nothing: every mount recorded in archivedMounts must have a
// same-named directory-type mount in newMounts, or nothing is renamed and an
// error is returned, so a tombstone is never left half-consumed. The
// destination directories are the empty ones ensureMountDirectories already
// created for the new workspace; each is removed immediately before its
// Rename since not every filesystem honors POSIX's allowance to rename a
// directory onto an existing empty one.
func restoreDirectoryMounts(root, archivedContainerPath, newWorkspaceID string, newMounts []domain.TemplateMount, archivedMounts []domain.RetainedDirectoryMount) error {
	if len(archivedMounts) == 0 {
		return nil
	}
	if root == "" || archivedContainerPath == "" {
		return ErrMountUnavailable
	}
	newByName := make(map[string]domain.TemplateMount, len(newMounts))
	for _, mount := range newMounts {
		if normalizedMountType(mount.Type) == domain.TemplateMountDirectory {
			newByName[mount.Name] = mount
		}
	}
	type move struct{ source, destination string }
	moves := make([]move, 0, len(archivedMounts))
	for _, archived := range archivedMounts {
		newMount, ok := newByName[archived.Name]
		if !ok {
			return fmt.Errorf("%w: no matching directory mount %q in the new template", ErrMountUnavailable, archived.Name)
		}
		archivedEntryName := archived.NamePrefix + archived.Name + archived.NameSuffix
		source, err := filepath.Abs(filepath.Join(archivedContainerPath, archivedEntryName))
		if err != nil {
			return ErrMountUnavailable
		}
		destination, err := filepath.Abs(filepath.Join(root, mountRootName(newWorkspaceID, newMount)))
		if err != nil {
			return ErrMountUnavailable
		}
		moves = append(moves, move{source: source, destination: destination})
	}
	for _, m := range moves {
		if _, err := os.Lstat(m.source); err != nil {
			return ErrMountUnavailable
		}
	}
	for _, m := range moves {
		// The destination is the empty directory ensureMountDirectories just
		// created. Remove it explicitly rather than relying on Rename to
		// replace an empty directory in place: that POSIX allowance is not
		// honored consistently by every filesystem this runs on.
		if err := os.Remove(m.destination); err != nil {
			return ErrMountUnavailable
		}
		if err := os.Rename(m.source, m.destination); err != nil {
			return ErrMountUnavailable
		}
	}
	return nil
}

type archiveActivity struct {
	Timestamp   string `json:"timestamp"`
	Action      string `json:"action"`
	WorkspaceID string `json:"workspace_id"`
	RuntimeID   string `json:"runtime_id,omitempty"`
	SourcePath  string `json:"source_path,omitempty"`
	ArchivePath string `json:"archive_path,omitempty"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}

func archiveActivityPaths(root, archiveRoot, workspaceID string) (string, string) {
	source := filepath.Join(root, managedContainerName(workspaceID))
	destination := filepath.Join(archiveRoot, managedContainerName(workspaceID))
	if absolute, err := filepath.Abs(source); err == nil {
		source = absolute
	}
	if absolute, err := filepath.Abs(destination); err == nil {
		destination = absolute
	}
	return source, destination
}

func (s *Service) logArchiveActivity(value domain.Workspace, action, status string, activityErr error) error {
	source, destination := archiveActivityPaths(s.mountRoot, s.mountArchiveRoot, value.ID)
	entry := archiveActivity{Action: action, WorkspaceID: value.ID, RuntimeID: value.RuntimeID, SourcePath: source, ArchivePath: destination, Status: status}
	if activityErr != nil {
		entry.Error = activityErr.Error()
	}
	return recordArchiveActivity(s.mountArchiveRoot, entry)
}

func archiveMountActivityAction(mounts []domain.TemplateMount) string {
	for _, mount := range mounts {
		if normalizedMountType(mount.Type) == domain.TemplateMountDirectory {
			return "managed_directory_archived"
		}
	}
	return "managed_directory_archive_skipped"
}

// recordArchiveActivity writes an append-only recovery trail outside the
// archived workspace directory. It contains identifiers and paths only.
func recordArchiveActivity(archiveRoot string, activity archiveActivity) error {
	if archiveRoot == "" {
		return ErrMountUnavailable
	}
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		return ErrMountUnavailable
	}
	if err := os.Chmod(archiveRoot, 0o700); err != nil {
		return ErrMountUnavailable
	}
	activity.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(activity)
	if err != nil {
		return fmt.Errorf("encode archive activity: %w", err)
	}
	data = append(data, '\n')
	file, err := os.OpenFile(filepath.Join(archiveRoot, "archive-activity.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return ErrMountUnavailable
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return ErrMountUnavailable
	}
	if _, err := file.Write(data); err != nil {
		return ErrMountUnavailable
	}
	if err := file.Sync(); err != nil {
		return ErrMountUnavailable
	}
	return nil
}

func removeMountDirectories(root, workspaceID string, mounts []domain.TemplateMount) error {
	needsDirectory := false
	for _, mount := range mounts {
		if normalizedMountType(mount.Type) == domain.TemplateMountDirectory {
			needsDirectory = true
			break
		}
	}
	if root == "" || !needsDirectory {
		return nil
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return ErrMountUnavailable
	}
	defer rootHandle.Close()
	if err := rootHandle.RemoveAll(managedContainerName(workspaceID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrMountUnavailable
	}
	return nil
}

// OpenRetainedDirectoryZip streams a bounded ZIP of a retained-directory
// tombstone's archived content without consuming the tombstone (self-service
// download, decision 0025). Ownership is verified before any filesystem
// access; actorID must be the tombstone's own owner, never an administrator
// bypass, since this is the self-service path, distinct from the
// administrator recovery routes.
func (s *Service) OpenRetainedDirectoryZip(ctx context.Context, actorID, workspaceID string) (io.ReadCloser, string, error) {
	user, err := s.requireActor(ctx, actorID)
	if err != nil {
		return nil, "", err
	}
	directory, err := s.store.FindRetainedWorkspaceDirectory(ctx, workspaceID, user.ID)
	if err != nil {
		return nil, "", err
	}
	if s.mountArchiveRoot == "" {
		return nil, "", ErrMountUnavailable
	}
	root, err := os.OpenRoot(s.mountArchiveRoot)
	if err != nil {
		return nil, "", ErrMountUnavailable
	}
	entryName := managedContainerName(directory.WorkspaceID)
	if _, err := root.Lstat(entryName); err != nil {
		root.Close()
		return nil, "", ErrMountUnavailable
	}
	reader, writer := io.Pipe()
	go func() {
		defer root.Close()
		zipWriter := zip.NewWriter(writer)
		state := archive.State{}
		walkErr := archive.WriteZip(ctx, root, entryName, zipWriter, &state)
		closeErr := zipWriter.Close()
		if walkErr != nil {
			_ = writer.CloseWithError(walkErr)
			return
		}
		_ = writer.CloseWithError(closeErr)
	}()
	name := directory.WorkspaceName
	if name == "" {
		name = directory.WorkspaceID
	}
	return reader, name, nil
}

// DeleteRetainedDirectory permanently discards a retained-directory
// tombstone and its archived content (self-service, decision 0025). The
// filesystem removal is best-effort, matching the compensating-cleanup
// pattern used elsewhere in this package (e.g. removeMountDirectories):
// the tombstone is already gone from the database by the time it runs, so a
// filesystem error here must not be reported as the operation having failed.
func (s *Service) DeleteRetainedDirectory(ctx context.Context, actorID, workspaceID string) error {
	user, err := s.requireActor(ctx, actorID)
	if err != nil {
		return err
	}
	directory, err := s.store.ConsumeRetainedWorkspaceDirectory(ctx, workspaceID, user.ID)
	if err != nil {
		return err
	}
	if s.mountArchiveRoot == "" {
		return nil
	}
	path, err := filepath.Abs(filepath.Join(s.mountArchiveRoot, managedContainerName(directory.WorkspaceID)))
	if err != nil {
		return nil
	}
	_ = os.RemoveAll(path)
	return nil
}
