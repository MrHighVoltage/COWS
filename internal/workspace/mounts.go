package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/runtime"
)

var ErrMountUnavailable = errors.New("workspace mount is unavailable")

type FileMount struct {
	Name          string
	ContainerPath string
	Root          string
	ReadOnly      bool
}

func (s *Service) ListFileMounts(ctx context.Context, actorID, workspaceID string) ([]FileMount, error) {
	value, err := s.GetWorkspace(ctx, actorID, workspaceID)
	if err != nil {
		return nil, err
	}
	if value.ObservedState != string(runtime.StateRunning) {
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
	root, err := filepath.Abs(s.mountRoot)
	if err != nil {
		return nil, ErrMountUnavailable
	}
	mounts := make([]FileMount, 0)
	for _, mount := range configuration.Mounts {
		if !mount.FileManager || normalizedMountType(mount.Type) != domain.TemplateMountDirectory {
			continue
		}
		mountRoot := filepath.Join(root, mountRootName(value.ID, mount))
		mounts = append(mounts, FileMount{Name: mount.Name, ContainerPath: mount.ContainerPath, Root: mountRoot, ReadOnly: mount.ReadOnly})
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

func managedMountName(workspaceID string, mount domain.TemplateMount) string {
	return "cows-" + workspaceID + "-" + mount.NamePrefix + mount.Name + mount.NameSuffix
}

func mountRootName(workspaceID string, mount domain.TemplateMount) string {
	return managedMountName(workspaceID, mount)
}

func materializeMount(root, workspaceID string, mount domain.TemplateMount) (runtime.Mount, error) {
	if mount.Name == "" {
		return runtime.Mount{}, ErrMountUnavailable
	}
	name := managedMountName(workspaceID, mount)
	switch normalizedMountType(mount.Type) {
	case domain.TemplateMountVolume:
		return runtime.Mount{Name: mount.Name, Type: domain.TemplateMountVolume, Source: name, ContainerPath: mount.ContainerPath, ReadOnly: mount.ReadOnly}, nil
	case domain.TemplateMountDirectory:
		if root == "" {
			return runtime.Mount{}, ErrMountUnavailable
		}
		path, err := filepath.Abs(filepath.Join(root, name))
		if err != nil {
			return runtime.Mount{}, ErrMountUnavailable
		}
		return runtime.Mount{Name: mount.Name, Type: "bind", Source: path, ContainerPath: mount.ContainerPath, ReadOnly: mount.ReadOnly}, nil
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
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return ErrMountUnavailable
	}
	defer rootHandle.Close()
	for _, mount := range mounts {
		if normalizedMountType(mount.Type) != domain.TemplateMountDirectory {
			continue
		}
		mode := os.FileMode(0o500)
		if !mount.ReadOnly {
			mode = 0o700
		}
		name := mountRootName(workspaceID, mount)
		if err := rootHandle.MkdirAll(name, mode); err != nil {
			return ErrMountUnavailable
		}
		if err := rootHandle.Chmod(name, mode); err != nil {
			return ErrMountUnavailable
		}
	}
	return nil
}

func archiveMountDirectories(root, workspaceID string, mounts []domain.TemplateMount) error {
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
	if err := rootHandle.MkdirAll("archive", 0o700); err != nil {
		return ErrMountUnavailable
	}
	for _, mount := range mounts {
		if normalizedMountType(mount.Type) != domain.TemplateMountDirectory {
			continue
		}
		name := mountRootName(workspaceID, mount)
		if _, err := rootHandle.Lstat(name); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return ErrMountUnavailable
		}
		if err := rootHandle.Rename(name, filepath.Join("archive", name)); err != nil {
			return ErrMountUnavailable
		}
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
	for _, mount := range mounts {
		if normalizedMountType(mount.Type) != domain.TemplateMountDirectory {
			continue
		}
		if err := rootHandle.RemoveAll(mountRootName(workspaceID, mount)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return ErrMountUnavailable
		}
	}
	return nil
}
