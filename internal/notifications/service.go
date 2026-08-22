package notifications

import (
	"context"
	"errors"
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
	Send(ctx context.Context, recipient, subject, body string) error
}

// LeadPolicy derives how far ahead of a deadline a warning is sent from the
// length of the window itself, so a one-hour stop window and a thirty-day
// retention window both get a proportionate notice.
type LeadPolicy struct {
	Divisor int
	Max     time.Duration
	Min     time.Duration
}

func (p LeadPolicy) Validate() error {
	if p.Divisor < 1 {
		return errors.New("email warning lead divisor must be at least 1")
	}
	if p.Min <= 0 || p.Max <= 0 {
		return errors.New("email warning lead bounds must be positive")
	}
	if p.Min > p.Max {
		return errors.New("email warning lead minimum must not exceed the maximum")
	}
	return nil
}

type Service struct {
	store         repository.Store
	sender        Sender
	leadPolicy    LeadPolicy
	retryInterval time.Duration
	now           func() time.Time
}

func New(store repository.Store, sender Sender, leadPolicy LeadPolicy, retryInterval time.Duration) (*Service, error) {
	if err := leadPolicy.Validate(); err != nil {
		return nil, err
	}
	if retryInterval <= 0 {
		return nil, errors.New("email retry interval must be positive")
	}
	return &Service{store: store, sender: sender, leadPolicy: leadPolicy, retryInterval: retryInterval, now: time.Now}, nil
}

// tokenURL builds a link into the configured external base URL. It refuses a
// base URL carrying a query or fragment so the token cannot be smuggled into an
// existing parameter.
func tokenURL(baseURL, path, rawToken string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid external base URL")
	}
	values := parsed.Query()
	values.Set("token", rawToken)
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

func (s *Service) EnqueuePasswordReset(ctx context.Context, userID, recipient, rawToken, baseURL string, expiresAt time.Time) error {
	if s.sender == nil || userID == "" || recipient == "" || rawToken == "" {
		return nil
	}
	resetURL, err := tokenURL(baseURL, "/password/reset/confirm", rawToken)
	if err != nil {
		return err
	}
	subject, body := PasswordResetMessage(resetURL, expiresAt)
	return s.queue(ctx, domain.EmailKindPasswordReset, userID, recipient, subject, body, "")
}

func (s *Service) EnqueueInvitation(ctx context.Context, userID, recipient, displayName, rawToken, baseURL string, expiresAt time.Time) error {
	if s.sender == nil || userID == "" || recipient == "" || rawToken == "" {
		return nil
	}
	acceptURL, err := tokenURL(baseURL, "/invitation/accept", rawToken)
	if err != nil {
		return err
	}
	subject, body := InvitationMessage(displayName, acceptURL, expiresAt)
	return s.queue(ctx, domain.EmailKindInvitation, userID, recipient, subject, body, "")
}

func (s *Service) EnqueueWelcome(ctx context.Context, userID, recipient, displayName, baseURL string) error {
	if s.sender == nil || userID == "" || recipient == "" {
		return nil
	}
	subject, body := WelcomeMessage(displayName, baseURL)
	return s.queue(ctx, domain.EmailKindWelcome, userID, recipient, subject, body, "")
}

func (s *Service) queue(ctx context.Context, kind, userID, recipient, subject, body, dedupeKey string) error {
	now := s.now().UTC()
	return s.store.UpsertEmailMessage(ctx, domain.EmailMessage{
		Kind: kind, UserID: userID, Recipient: recipient, Subject: subject, Body: body,
		DedupeKey: dedupeKey, Status: "pending", NextAttemptAt: now, CreatedAt: now,
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
		window := time.Duration(0)
		switch status.Phase {
		case workspace.TimeoutPhaseAwaitingConnection:
			kind = domain.EmailKindTimeoutStop
			window = time.Duration(value.InitialConnectionTimeoutSeconds) * time.Second
		case workspace.TimeoutPhaseStoppedRetention:
			kind = domain.EmailKindTimeoutDelete
			window = time.Duration(value.StoppedRetentionSeconds) * time.Second
		}
		if kind == "" || status.Deadline.IsZero() || !status.Deadline.After(now) {
			continue
		}
		lead, send := s.leadPolicy.Lead(window)
		if !send || status.Deadline.Sub(now) > lead {
			continue
		}
		owner, err := s.store.FindUserByID(ctx, value.OwnerUserID)
		if err != nil || owner.Disabled || owner.Email == "" {
			continue
		}
		action := "stopped"
		if kind == domain.EmailKindTimeoutDelete {
			action = "deleted"
		}
		subject, body := TimeoutWarningMessage(value.Name, action, status.Deadline)
		if err := s.queue(ctx, kind, value.OwnerUserID, owner.Email, subject, body, workspaceDedupeKey(value.ID, kind)); err != nil {
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
	messages, err := s.store.ListPendingEmailMessages(ctx, now, maxBatchSize)
	if err != nil {
		return err
	}
	failed := false
	for _, message := range messages {
		current, err := s.messageIsCurrent(ctx, message, now)
		if err != nil {
			return err
		}
		if !current {
			if err := s.store.MarkEmailMessageCanceled(ctx, message.ID); err != nil && !errors.Is(err, repository.ErrNotFound) {
				return err
			}
			continue
		}
		if err := s.sender.Send(ctx, message.Recipient, message.Subject, message.Body); err != nil {
			failed = true
			attempts := message.Attempts + 1
			if err := s.store.MarkEmailMessageFailed(ctx, message.ID, attempts, now.Add(s.retryInterval), "smtp_send_failed"); err != nil {
				return err
			}
			if attempts >= maxDeliveryAttempts {
				if err := s.store.MarkEmailMessageCanceled(ctx, message.ID); err != nil && !errors.Is(err, repository.ErrNotFound) {
					return err
				}
			}
			continue
		}
		if err := s.store.MarkEmailMessageSent(ctx, message.ID, now); err != nil {
			return err
		}
	}
	if failed {
		return ErrDeliveryFailed
	}
	return nil
}

func workspaceDedupeKey(workspaceID, kind string) string {
	return "workspace:" + workspaceID + ":" + kind
}

// workspaceIDFromDedupeKey parses "workspace:<id>:<kind>". Workspace IDs are
// URL-safe base64 and never contain a colon.
func workspaceIDFromDedupeKey(key string) (string, bool) {
	parts := strings.Split(key, ":")
	if len(parts) != 3 || parts[0] != "workspace" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// messageIsCurrent re-checks the two lifecycle warnings against live state so a
// warning is not delivered after the deadline it describes has moved or the
// workspace has already been acted on. Every other kind is a statement about
// something that already happened and is always current.
func (s *Service) messageIsCurrent(ctx context.Context, message domain.EmailMessage, now time.Time) (bool, error) {
	if message.Kind != domain.EmailKindTimeoutStop && message.Kind != domain.EmailKindTimeoutDelete {
		return true, nil
	}
	workspaceID, ok := workspaceIDFromDedupeKey(message.DedupeKey)
	if !ok {
		return false, nil
	}
	value, err := s.store.FindWorkspaceByID(ctx, workspaceID)
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
	if owner.Disabled || owner.Email == "" || owner.Email != message.Recipient {
		return false, nil
	}
	status := workspace.EvaluateTimeouts(value, now)
	if status.Deadline.IsZero() {
		return false, nil
	}
	if message.Kind == domain.EmailKindTimeoutStop {
		return status.Phase == workspace.TimeoutPhaseAwaitingConnection && value.ObservedState == string(runtime.StateRunning), nil
	}
	return status.Phase == workspace.TimeoutPhaseStoppedRetention && value.RuntimeID != "", nil
}
