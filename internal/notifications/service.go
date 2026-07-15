package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
	"github.com/cows-project/cows/internal/runtime"
	"github.com/cows-project/cows/internal/workspace"
)

const (
	maxDeliveryAttempts = 5
	maxBatchSize        = 50
)

var ErrDeliveryFailed = errors.New("email notification delivery failed")

type Sender interface {
	Send(ctx context.Context, notification domain.EmailNotification) error
}

type Service struct {
	store         repository.Store
	sender        Sender
	warningLead   time.Duration
	retryInterval time.Duration
	now           func() time.Time
}

func New(store repository.Store, sender Sender, warningLead, retryInterval time.Duration) (*Service, error) {
	if warningLead <= 0 {
		return nil, errors.New("email warning lead time must be positive")
	}
	if retryInterval <= 0 {
		return nil, errors.New("email retry interval must be positive")
	}
	return &Service{store: store, sender: sender, warningLead: warningLead, retryInterval: retryInterval, now: time.Now}, nil
}

func (s *Service) EnqueueTimeoutWarnings(ctx context.Context) error {
	if s.sender == nil {
		return nil
	}
	now := s.now().UTC()
	values, err := s.store.ListAllWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, value := range values {
		status := workspace.EvaluateTimeouts(value, now)
		kind := ""
		switch status.Phase {
		case workspace.TimeoutPhaseAwaitingConnection:
			kind = domain.NotificationTimeoutStop
		case workspace.TimeoutPhaseStoppedRetention:
			kind = domain.NotificationTimeoutDelete
		}
		if kind == "" || status.Deadline.IsZero() || !status.Deadline.After(now) || status.Deadline.Sub(now) > s.warningLead {
			continue
		}
		owner, err := s.store.FindUserByID(ctx, value.OwnerUserID)
		if err != nil || owner.Disabled || owner.Email == "" {
			continue
		}
		action := "stopped"
		if kind == domain.NotificationTimeoutDelete {
			action = "deleted"
		}
		notification := domain.EmailNotification{
			WorkspaceID:   value.ID,
			OwnerUserID:   value.OwnerUserID,
			Recipient:     owner.Email,
			Kind:          kind,
			Deadline:      status.Deadline,
			Subject:       "COWS workspace lifecycle warning",
			Body:          fmt.Sprintf("Your COWS workspace %q is scheduled to be %s automatically at %s UTC.\n\nThis is an advisory notification; the workspace policy remains authoritative.", value.Name, action, status.Deadline.UTC().Format("2006-01-02 15:04")),
			Status:        "pending",
			NextAttemptAt: now,
			CreatedAt:     now,
		}
		if err := s.store.UpsertEmailNotification(ctx, notification); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Deliver(ctx context.Context) error {
	if s.sender == nil {
		return nil
	}
	now := s.now().UTC()
	notifications, err := s.store.ListPendingEmailNotifications(ctx, now, maxBatchSize)
	if err != nil {
		return err
	}
	failed := false
	for _, notification := range notifications {
		if !s.notificationIsCurrent(ctx, notification, now) {
			_ = s.store.MarkEmailNotificationCanceled(ctx, notification.ID)
			continue
		}
		if err := s.sender.Send(ctx, notification); err != nil {
			failed = true
			attempts := notification.Attempts + 1
			_ = s.store.MarkEmailNotificationFailed(ctx, notification.ID, attempts, now.Add(s.retryInterval), "smtp_send_failed")
			if attempts >= maxDeliveryAttempts {
				_ = s.store.MarkEmailNotificationCanceled(ctx, notification.ID)
			}
			continue
		}
		if err := s.store.MarkEmailNotificationSent(ctx, notification.ID, now); err != nil {
			return err
		}
	}
	if failed {
		return ErrDeliveryFailed
	}
	return nil
}

func (s *Service) notificationIsCurrent(ctx context.Context, notification domain.EmailNotification, now time.Time) bool {
	value, err := s.store.FindWorkspaceByID(ctx, notification.WorkspaceID)
	if errors.Is(err, repository.ErrNotFound) || err != nil {
		return false
	}
	status := workspace.EvaluateTimeouts(value, now)
	if status.Deadline.IsZero() || status.Deadline.Unix() != notification.Deadline.Unix() {
		return false
	}
	if notification.Kind == domain.NotificationTimeoutStop {
		return status.Phase == workspace.TimeoutPhaseAwaitingConnection && value.ObservedState == string(runtime.StateRunning)
	}
	return notification.Kind == domain.NotificationTimeoutDelete && status.Phase == workspace.TimeoutPhaseStoppedRetention && value.RuntimeID != ""
}
