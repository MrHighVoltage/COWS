package domain

import "time"

// Email message kinds. Every queued message carries exactly one.
const (
	EmailKindTimeoutStop      = "timeout_stop"
	EmailKindTimeoutDelete    = "timeout_delete"
	EmailKindWorkspaceDeleted = "workspace_deleted"
	EmailKindPasswordReset    = "password_reset"
	EmailKindInvitation       = "invitation"
	EmailKindWelcome          = "welcome"
)

// EmailMessage is a queued outbound message. Bodies never contain a password,
// session secret, runtime identifier, host path, or volume name.
type EmailMessage struct {
	ID        int64
	Kind      string
	UserID    string
	Recipient string
	Subject   string
	Body      string
	// DedupeKey is empty for one-shot mail. A non-empty value is unique across
	// the table, so re-enqueueing updates in place instead of queueing a
	// second copy.
	DedupeKey     string
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LastErrorCode string
	CreatedAt     time.Time
	SentAt        time.Time
}
