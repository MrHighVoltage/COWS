package workspace

import (
	"context"
	"io"
	"sync"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/runtime"
)

// workspaceConnection owns both the runtime stream and its session count.
// Even a failed WebSocket upgrade closes this one object exactly once.
type workspaceConnection struct {
	io.ReadWriteCloser
	once sync.Once
	end  func()
	err  error
}

func (c *workspaceConnection) Close() error {
	c.once.Do(func() {
		c.err = c.ReadWriteCloser.Close()
		c.end()
	})
	return c.err
}

type workspaceTerminal struct {
	runtime.Terminal
	connection *workspaceConnection
}

func (t *workspaceTerminal) Close() error { return t.connection.Close() }

// trackWorkspaceConnection is called while the workspace lock is held. The
// generation changes on every new running period, including same-second starts.
func (s *Service) trackWorkspaceConnection(stream io.ReadWriteCloser, actorID, workspaceID, eventType string) *workspaceConnection {
	generation := s.lockForWorkspace(workspaceID).sessionGeneration
	return &workspaceConnection{ReadWriteCloser: stream, end: func() {
		s.recordWorkspaceDisconnect(context.Background(), actorID, workspaceID, eventType, generation)
	}}
}

func (s *Service) recordWorkspaceDisconnect(ctx context.Context, actorID, workspaceID, eventType string, generation uint64) {
	release, err := s.beginLifecycleChange(ctx, workspaceID)
	if err != nil {
		return
	}
	defer release()
	if generation == s.lockForWorkspace(workspaceID).sessionGeneration {
		now := s.now().UTC()
		_ = s.store.RecordWorkspaceSessionEnd(ctx, workspaceID, now, now)
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: eventType, TargetType: "workspace", TargetID: workspaceID})
}
