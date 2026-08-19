package workspace

import (
	"context"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/domain"
)

func TestEvaluateTimeouts(t *testing.T) {
	base := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		value  domain.Workspace
		now    time.Time
		phase  TimeoutPhase
		action TimeoutAction
		due    bool
	}{
		{
			name:  "initial connection warning window",
			value: domain.Workspace{ObservedState: "running", StartedAt: base, IdleSince: base, InitialConnectionTimeoutSeconds: 3600},
			now:   base.Add(30 * time.Minute), phase: TimeoutPhaseAwaitingConnection, action: TimeoutActionNone,
		},
		{
			name:  "initial connection stop",
			value: domain.Workspace{ObservedState: "running", StartedAt: base, IdleSince: base, InitialConnectionTimeoutSeconds: 3600},
			now:   base.Add(time.Hour), phase: TimeoutPhaseAwaitingConnection, action: TimeoutActionStop, due: true,
		},
		{
			name:  "connected workspace is never a timeout candidate no matter how long the session lasts",
			value: domain.Workspace{ObservedState: "running", StartedAt: base, LastConnectedAt: base.Add(10 * time.Minute), ActiveSessions: 1, InitialConnectionTimeoutSeconds: 3600},
			now:   base.Add(2 * time.Hour), phase: TimeoutPhaseNone, action: TimeoutActionNone,
		},
		{
			name:  "disconnected after a prior connection is a stop candidate again, measured from the disconnect",
			value: domain.Workspace{ObservedState: "running", StartedAt: base, LastConnectedAt: base.Add(10 * time.Minute), ActiveSessions: 0, IdleSince: base.Add(20 * time.Minute), InitialConnectionTimeoutSeconds: 3600},
			now:   base.Add(20*time.Minute + time.Hour), phase: TimeoutPhaseAwaitingConnection, action: TimeoutActionStop, due: true,
		},
		{
			name:  "disconnected but still within the idle grace period is not yet due",
			value: domain.Workspace{ObservedState: "running", StartedAt: base, LastConnectedAt: base.Add(10 * time.Minute), ActiveSessions: 0, IdleSince: base.Add(20 * time.Minute), InitialConnectionTimeoutSeconds: 3600},
			now:   base.Add(30 * time.Minute), phase: TimeoutPhaseAwaitingConnection, action: TimeoutActionNone,
		},
		{
			name:  "stopped retention deletion",
			value: domain.Workspace{ObservedState: "exited", RuntimeID: "container-1", StoppedAt: base, StoppedRetentionSeconds: 3600},
			now:   base.Add(time.Hour), phase: TimeoutPhaseStoppedRetention, action: TimeoutActionDelete, due: true,
		},
		{
			name:  "deleted workspace has no automatic data action",
			value: domain.Workspace{ContainerDeletedAt: base},
			now:   base.Add(time.Hour), phase: TimeoutPhaseNone, action: TimeoutActionNone,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := EvaluateTimeouts(test.value, test.now)
			if status.Phase != test.phase || status.Action != test.action || status.Due != test.due {
				t.Fatalf("status = %+v, want phase=%q action=%q due=%t", status, test.phase, test.action, test.due)
			}
		})
	}
}

func TestEvaluateTimeoutsDoesNotDeleteAmbiguousStoppedState(t *testing.T) {
	status := EvaluateTimeouts(domain.Workspace{ObservedState: "unknown", StoppedAt: time.Now().UTC(), StoppedRetentionSeconds: 1}, time.Now().Add(time.Hour))
	if status.Action != TimeoutActionNone || status.Due {
		t.Fatalf("ambiguous state produced destructive action: %+v", status)
	}
}

// TestSessionTrackingReArmsIdleTimeoutAfterDisconnect exercises the actual
// bug being fixed here end to end through the real store:
// RecordWorkspaceConnection/RecordTerminalDisconnect/RecordDesktopDisconnect
// must track how many sessions are currently open, not merely whether the
// workspace has ever been connected to - a workspace that connects once and
// later disconnects must become a stop candidate again, measured from the
// disconnect, not stay permanently exempt.
func TestSessionTrackingReArmsIdleTimeoutAfterDisconnect(t *testing.T) {
	ctx := context.Background()
	_, authService, adminID, store := testService(t)
	service := NewWithRuntimeAndMountRoots(store, &lifecycleRuntime{}, t.TempDir(), t.TempDir())

	template, err := service.CreateTemplate(ctx, adminID, validTemplateInput())
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	owner := newReattachUser(t, authService, adminID, "session-tracking-owner")
	value, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{Name: "session tracking workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	base := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	if err := store.UpdateWorkspaceObservedState(ctx, value.ID, "running", "runtime-session-tracking", "", "", base, base); err != nil {
		t.Fatalf("mark workspace running: %v", err)
	}
	if err := store.UpdateWorkspaceLifecycle(ctx, value.ID, base, time.Time{}, time.Time{}, time.Time{}, time.Time{}, base); err != nil {
		t.Fatalf("set started_at: %v", err)
	}
	if err := store.ResetWorkspaceSessions(ctx, value.ID, base, base); err != nil {
		t.Fatalf("reset sessions to the just-started state: %v", err)
	}

	reload := func() domain.Workspace {
		t.Helper()
		reloaded, err := service.GetWorkspace(ctx, owner.ID, value.ID)
		if err != nil {
			t.Fatalf("reload workspace: %v", err)
		}
		return reloaded
	}

	// validTemplateInput sets InitialConnectionTimeoutSeconds to 2 hours;
	// CreateWorkspace copies it from the template onto the workspace row.
	if status := EvaluateTimeouts(reload(), base.Add(3*time.Hour)); status.Phase != TimeoutPhaseAwaitingConnection || status.Action != TimeoutActionStop {
		t.Fatalf("fresh idle workspace status = %+v, want a due stop", status)
	}

	// Two overlapping sessions open (terminal + desktop): connecting must
	// cancel the timeout, and it must stay cancelled while either is open.
	if err := service.RecordWorkspaceConnection(ctx, owner.ID, value.ID); err != nil {
		t.Fatalf("record terminal connection: %v", err)
	}
	if err := service.RecordWorkspaceConnection(ctx, owner.ID, value.ID); err != nil {
		t.Fatalf("record desktop connection: %v", err)
	}
	if reloaded := reload(); reloaded.ActiveSessions != 2 || !reloaded.IdleSince.IsZero() {
		t.Fatalf("after two connections: active_sessions=%d idle_since=%v, want 2 and zero", reloaded.ActiveSessions, reloaded.IdleSince)
	}

	// Closing the first of two sessions must not yet re-arm the timeout.
	service.RecordTerminalDisconnect(ctx, owner.ID, value.ID)
	if reloaded := reload(); reloaded.ActiveSessions != 1 || !reloaded.IdleSince.IsZero() {
		t.Fatalf("after closing one of two sessions: active_sessions=%d idle_since=%v, want 1 and zero", reloaded.ActiveSessions, reloaded.IdleSince)
	}

	// Closing the last session re-arms the timeout from this moment - not
	// from the original start, and not permanently disabled by having ever
	// connected (the bug being fixed here).
	disconnectAt := base.Add(5 * time.Hour)
	service.now = func() time.Time { return disconnectAt }
	service.RecordDesktopDisconnect(ctx, owner.ID, value.ID)
	afterDisconnect := reload()
	if afterDisconnect.ActiveSessions != 0 || !afterDisconnect.IdleSince.Equal(disconnectAt) {
		t.Fatalf("after closing the last session: active_sessions=%d idle_since=%v, want 0 and %v", afterDisconnect.ActiveSessions, afterDisconnect.IdleSince, disconnectAt)
	}
	if status := EvaluateTimeouts(afterDisconnect, disconnectAt.Add(time.Hour)); status.Phase != TimeoutPhaseAwaitingConnection || status.Action != TimeoutActionNone {
		t.Fatalf("status shortly after disconnect = %+v, want awaiting_connection/none (still within the 2-hour idle grace period)", status)
	}
	if status := EvaluateTimeouts(afterDisconnect, disconnectAt.Add(3*time.Hour)); status.Phase != TimeoutPhaseAwaitingConnection || status.Action != TimeoutActionStop || !status.Due {
		t.Fatalf("status after the idle grace period elapses = %+v, want a due stop", status)
	}
}
