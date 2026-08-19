package workspace

import (
	"time"

	"github.com/cows-project/cows/internal/domain"
)

type TimeoutAction string

const (
	TimeoutActionNone   TimeoutAction = "none"
	TimeoutActionStop   TimeoutAction = "stop"
	TimeoutActionDelete TimeoutAction = "delete"
)

type TimeoutPhase string

const (
	TimeoutPhaseNone               TimeoutPhase = "none"
	TimeoutPhaseAwaitingConnection TimeoutPhase = "awaiting_connection"
	TimeoutPhaseStoppedRetention   TimeoutPhase = "stopped_retention"
)

type TimeoutStatus struct {
	Phase    TimeoutPhase
	Action   TimeoutAction
	Deadline time.Time
	Due      bool
}

func EvaluateTimeouts(value domain.Workspace, now time.Time) TimeoutStatus {
	now = now.UTC()
	if !value.StoppedAt.IsZero() && value.RuntimeID != "" && value.StoppedRetentionSeconds > 0 {
		deadline := value.StoppedAt.Add(time.Duration(value.StoppedRetentionSeconds) * time.Second)
		status := TimeoutStatus{Phase: TimeoutPhaseStoppedRetention, Action: TimeoutActionNone, Deadline: deadline}
		if !now.Before(deadline) {
			status.Action = TimeoutActionDelete
			status.Due = true
		}
		return status
	}
	// A running workspace with no open session (ActiveSessions == 0) is a
	// stop candidate once it has been idle - never yet connected, or
	// disconnected since - for InitialConnectionTimeoutSeconds. IdleSince is
	// cleared to zero for as long as any session is open (see
	// RecordWorkspaceSessionStart), so a connected workspace never reaches
	// this branch regardless of connection duration - only the elapsed time
	// since the workspace last had zero open sessions matters.
	if value.ObservedState == "running" && value.ActiveSessions == 0 && !value.IdleSince.IsZero() && value.InitialConnectionTimeoutSeconds > 0 {
		deadline := value.IdleSince.Add(time.Duration(value.InitialConnectionTimeoutSeconds) * time.Second)
		status := TimeoutStatus{Phase: TimeoutPhaseAwaitingConnection, Action: TimeoutActionNone, Deadline: deadline}
		if !now.Before(deadline) {
			status.Action = TimeoutActionStop
			status.Due = true
		}
		return status
	}
	return TimeoutStatus{Phase: TimeoutPhaseNone, Action: TimeoutActionNone}
}
