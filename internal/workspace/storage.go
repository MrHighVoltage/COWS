package workspace

import (
	"context"
	"path/filepath"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/runtime"
)

// StorageUsageProvider resolves only COWS-managed sources. It never accepts a
// path from the browser or from a workspace container.
type StorageUsageProvider struct {
	runtime   runtime.StorageUsageRuntime
	mountRoot string
}

func NewStorageUsageProvider(runtimeAdapter runtime.StorageUsageRuntime, mountRoot string) *StorageUsageProvider {
	return &StorageUsageProvider{runtime: runtimeAdapter, mountRoot: mountRoot}
}

func (p *StorageUsageProvider) WorkspaceStorageUsage(ctx context.Context, value domain.Workspace) (int64, error) {
	if p == nil || p.runtime == nil {
		return 0, runtime.ErrUnavailable
	}
	root, err := filepath.Abs(p.mountRoot)
	if err != nil {
		return 0, err
	}
	mounts := make([]runtime.FileAccessSpec, 0, len(value.TemplateConfiguration.Mounts))
	for _, mount := range value.TemplateConfiguration.Mounts {
		mountType := normalizedMountType(mount.Type)
		source := managedVolumeName(value.ID, mount)
		if mountType == domain.TemplateMountDirectory {
			source = filepath.Join(root, mountRootName(value.ID, mount))
		}
		mounts = append(mounts, runtime.FileAccessSpec{
			MountType:     mountType,
			Source:        source,
			ContainerPath: mount.ContainerPath,
			ContainerUID:  0,
			ContainerGID:  0,
			ReadOnly:      true,
		})
	}
	runtimeID := value.RuntimeID
	if value.ObservedState == string(runtime.StateRemoved) || value.ObservedState == "missing" {
		// Timeout cleanup may remove the container while deliberately leaving
		// managed data in place. The helper can still measure those mounts.
		runtimeID = ""
	}
	return p.runtime.WorkspaceStorageUsage(ctx, runtime.StorageUsageSpec{RuntimeID: runtimeID, Mounts: mounts})
}
