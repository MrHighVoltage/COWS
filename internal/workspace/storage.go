package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/runtime"
)

// StorageUsageProvider resolves only COWS-managed sources. It never accepts a
// path from the browser or from a workspace container.
type StorageUsageProvider struct {
	runtime   runtime.StorageUsageRuntime
	mountRoot string
	mu        sync.Mutex
	cache     map[string]*storageUsageCacheEntry
}

const (
	storageUsageCacheTTL      = 30 * time.Second
	storageUsageErrorCacheTTL = 5 * time.Second
	maxStorageUsageCache      = 1024
)

type storageUsageCacheEntry struct {
	fingerprint string
	usage       int64
	err         error
	measuredAt  time.Time
	done        chan struct{}
}

func NewStorageUsageProvider(runtimeAdapter runtime.StorageUsageRuntime, mountRoot string) *StorageUsageProvider {
	return &StorageUsageProvider{runtime: runtimeAdapter, mountRoot: mountRoot, cache: make(map[string]*storageUsageCacheEntry)}
}

func (p *StorageUsageProvider) WorkspaceStorageUsage(ctx context.Context, value domain.Workspace) (int64, error) {
	if p == nil || p.runtime == nil {
		return 0, runtime.ErrUnavailable
	}
	fingerprint := value.RuntimeID + "\x00" + value.ObservedState
	for {
		p.mu.Lock()
		if p.cache == nil {
			p.cache = make(map[string]*storageUsageCacheEntry)
		}
		entry := p.cache[value.ID]
		if entry != nil && entry.fingerprint == fingerprint {
			if entry.done != nil {
				wait := entry.done
				p.mu.Unlock()
				select {
				case <-wait:
					continue
				case <-ctx.Done():
					return 0, ctx.Err()
				}
			}
			ttl := storageUsageCacheTTL
			if entry.err != nil {
				ttl = storageUsageErrorCacheTTL
			}
			if time.Since(entry.measuredAt) < ttl {
				usage, err := entry.usage, entry.err
				p.mu.Unlock()
				return usage, err
			}
		}
		if entry == nil && len(p.cache) >= maxStorageUsageCache {
			for key, cached := range p.cache {
				if cached.done == nil {
					delete(p.cache, key)
					break
				}
			}
		}
		entry = &storageUsageCacheEntry{fingerprint: fingerprint, done: make(chan struct{})}
		p.cache[value.ID] = entry
		p.mu.Unlock()

		usage, err := p.measure(ctx, value)
		p.mu.Lock()
		entry.usage = usage
		entry.err = err
		entry.measuredAt = time.Now()
		close(entry.done)
		entry.done = nil
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if p.cache[value.ID] == entry {
				delete(p.cache, value.ID)
			}
		}
		p.mu.Unlock()
		return usage, err
	}
}

func (p *StorageUsageProvider) measure(ctx context.Context, value domain.Workspace) (int64, error) {
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
