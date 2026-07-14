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
	if value.ObservedState == "running" && value.LastConnectedAt.IsZero() && !value.StartedAt.IsZero() && value.InitialConnectionTimeoutSeconds > 0 {
		deadline := value.StartedAt.Add(time.Duration(value.InitialConnectionTimeoutSeconds) * time.Second)
		status := TimeoutStatus{Phase: TimeoutPhaseAwaitingConnection, Action: TimeoutActionNone, Deadline: deadline}
		if !now.Before(deadline) {
			status.Action = TimeoutActionStop
			status.Due = true
		}
		return status
	}
	return TimeoutStatus{Phase: TimeoutPhaseNone, Action: TimeoutActionNone}
}
