package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

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

func materializeMount(root, workspaceID string, mount domain.TemplateMount) (runtime.Mount, error) {
	if mount.Name == "" {
		return runtime.Mount{}, ErrMountUnavailable
	}
	switch normalizedMountType(mount.Type) {
	case domain.TemplateMountVolume:
		return runtime.Mount{Name: mount.Name, Type: domain.TemplateMountVolume, Source: managedVolumeName(workspaceID, mount), ContainerPath: mount.ContainerPath, ReadOnly: mount.ReadOnly}, nil
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
