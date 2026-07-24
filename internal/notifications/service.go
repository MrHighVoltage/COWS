package notifications

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
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

type MessageSender interface {
	SendMessage(ctx context.Context, recipient, subject, body string) error
}

type Service struct {
	store         repository.Store
	sender        Sender
	messageSender MessageSender
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
	service := &Service{store: store, sender: sender, warningLead: warningLead, retryInterval: retryInterval, now: time.Now}
	if messageSender, ok := sender.(MessageSender); ok {
		service.messageSender = messageSender
	}
	return service, nil
}

func (s *Service) EnqueuePasswordReset(ctx context.Context, requestUserID, recipient, rawToken, baseURL string, expiresAt time.Time) error {
	if s.messageSender == nil || requestUserID == "" || recipient == "" || rawToken == "" {
		return nil
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("invalid password reset base URL")
	}
	values := parsed.Query()
	values.Set("token", rawToken)
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/password/reset/confirm"
	parsed.RawQuery = values.Encode()
	now := s.now().UTC()
	return s.store.UpsertPasswordResetEmail(ctx, domain.PasswordResetEmail{
		UserID: requestUserID, Recipient: recipient, Subject: "COWS password reset",
		Body:   fmt.Sprintf("A COWS password reset was requested for your account. Open this link within one hour to choose a new password:\n\n%s\n\nIf you did not request this, ignore this message.", parsed.String()),
		Status: "pending", NextAttemptAt: now, CreatedAt: now,
	})
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
	if s.sender == nil && s.messageSender == nil {
		return nil
	}
	now := s.now().UTC()
	failed := false
	if s.sender != nil {
		notifications, err := s.store.ListPendingEmailNotifications(ctx, now, maxBatchSize)
		if err != nil {
			return err
		}
		for _, notification := range notifications {
			current, err := s.notificationIsCurrent(ctx, notification, now)
			if err != nil {
				return err
			}
			if !current {
				if err := s.store.MarkEmailNotificationCanceled(ctx, notification.ID); err != nil && !errors.Is(err, repository.ErrNotFound) {
					return err
				}
				continue
			}
			if err := s.sender.Send(ctx, notification); err != nil {
				failed = true
				attempts := notification.Attempts + 1
				if err := s.store.MarkEmailNotificationFailed(ctx, notification.ID, attempts, now.Add(s.retryInterval), "smtp_send_failed"); err != nil {
					return err
				}
				if attempts >= maxDeliveryAttempts {
					if err := s.store.MarkEmailNotificationCanceled(ctx, notification.ID); err != nil && !errors.Is(err, repository.ErrNotFound) {
						return err
					}
				}
				continue
			}
			if err := s.store.MarkEmailNotificationSent(ctx, notification.ID, now); err != nil {
				return err
			}
		}
	}
	if s.messageSender != nil {
		messages, err := s.store.ListPendingPasswordResetEmails(ctx, now, maxBatchSize)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if err := s.messageSender.SendMessage(ctx, message.Recipient, message.Subject, message.Body); err != nil {
				failed = true
				attempts := message.Attempts + 1
				if attempts >= maxDeliveryAttempts {
					if err := s.store.MarkPasswordResetEmailCanceled(ctx, message.ID); err != nil && !errors.Is(err, repository.ErrNotFound) {
						return err
					}
				} else if err := s.store.MarkPasswordResetEmailFailed(ctx, message.ID, attempts, now.Add(s.retryInterval), "smtp_send_failed"); err != nil {
					return err
				}
				continue
			}
			if err := s.store.MarkPasswordResetEmailSent(ctx, message.ID, now); err != nil {
				return err
			}
		}
	}
	if failed {
		return ErrDeliveryFailed
	}
	return nil
}

func (s *Service) notificationIsCurrent(ctx context.Context, notification domain.EmailNotification, now time.Time) (bool, error) {
	value, err := s.store.FindWorkspaceByID(ctx, notification.WorkspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	owner, err := s.store.FindUserByID(ctx, value.OwnerUserID)
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if owner.Disabled || owner.Email == "" || owner.Email != notification.Recipient {
		return false, nil
	}
	status := workspace.EvaluateTimeouts(value, now)
	if status.Deadline.IsZero() || status.Deadline.Unix() != notification.Deadline.Unix() {
		return false, nil
	}
	if notification.Kind == domain.NotificationTimeoutStop {
		return status.Phase == workspace.TimeoutPhaseAwaitingConnection && value.ObservedState == string(runtime.StateRunning), nil
	}
	return notification.Kind == domain.NotificationTimeoutDelete && status.Phase == workspace.TimeoutPhaseStoppedRetention && value.RuntimeID != "", nil
}
