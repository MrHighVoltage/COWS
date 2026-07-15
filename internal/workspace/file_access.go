package workspace

import (
	"context"

	"github.com/cows-project/cows/internal/runtime"
)

// workspaceLock serializes file operations with lifecycle changes. Streaming
// downloads hold the lock until their returned reader is closed.
type workspaceLock struct {
	semaphore chan struct{}
}

func (s *Service) lockForWorkspace(workspaceID string) *workspaceLock {
	value, _ := s.fileAccessLocks.LoadOrStore(workspaceID, &workspaceLock{semaphore: make(chan struct{}, 1)})
	return value.(*workspaceLock)
}

func (s *Service) acquireWorkspaceLock(ctx context.Context, workspaceID string) (func(), error) {
	lock := s.lockForWorkspace(workspaceID)
	select {
	case lock.semaphore <- struct{}{}:
		return func() { <-lock.semaphore }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// BeginFileAccess authenticates the actor and reserves the workspace against
// lifecycle changes until release is called.
func (s *Service) BeginFileAccess(ctx context.Context, actorID, workspaceID string) (func(), error) {
	release, err := s.acquireWorkspaceLock(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	value, err := s.GetWorkspace(ctx, actorID, workspaceID)
	if err != nil {
		release()
		return nil, err
	}
	switch value.ObservedState {
	case string(runtime.StateRunning), string(runtime.StateStopped), string(runtime.StateExited):
		return release, nil
	default:
		release()
		return nil, ErrFileManagerNotAvailable
	}
}

func (s *Service) beginLifecycleChange(ctx context.Context, workspaceID string) (func(), error) {
	return s.acquireWorkspaceLock(ctx, workspaceID)
}
