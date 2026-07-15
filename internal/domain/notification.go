package domain

import "time"

const (
	NotificationTimeoutStop   = "timeout_stop"
	NotificationTimeoutDelete = "timeout_delete"
)

type EmailNotification struct {
	ID            int64
	WorkspaceID   string
	OwnerUserID   string
	Recipient     string
	Kind          string
	Deadline      time.Time
	Subject       string
	Body          string
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LastErrorCode string
	CreatedAt     time.Time
	SentAt        time.Time
}
