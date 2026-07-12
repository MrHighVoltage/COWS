package workspace

import (
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
			value: domain.Workspace{ObservedState: "running", StartedAt: base, InitialConnectionTimeoutSeconds: 3600},
			now:   base.Add(30 * time.Minute), phase: TimeoutPhaseAwaitingConnection, action: TimeoutActionNone,
		},
		{
			name:  "initial connection stop",
			value: domain.Workspace{ObservedState: "running", StartedAt: base, InitialConnectionTimeoutSeconds: 3600},
			now:   base.Add(time.Hour), phase: TimeoutPhaseAwaitingConnection, action: TimeoutActionStop, due: true,
		},
		{
			name:  "connected workspace is not stopped for no connection",
			value: domain.Workspace{ObservedState: "running", StartedAt: base, LastConnectedAt: base.Add(10 * time.Minute), InitialConnectionTimeoutSeconds: 3600},
			now:   base.Add(2 * time.Hour), phase: TimeoutPhaseNone, action: TimeoutActionNone,
		},
		{
			name:  "stopped retention deletion",
			value: domain.Workspace{ObservedState: "exited", RuntimeID: "container-1", StoppedAt: base, StoppedRetentionSeconds: 3600},
			now:   base.Add(time.Hour), phase: TimeoutPhaseStoppedRetention, action: TimeoutActionDelete, due: true,
		},
		{
			name:  "data archive eligibility",
			value: domain.Workspace{ContainerDeletedAt: base, DataRetentionSeconds: 3600},
			now:   base.Add(time.Hour), phase: TimeoutPhaseArchiveEligible, action: TimeoutActionArchiveEligible, due: true,
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
