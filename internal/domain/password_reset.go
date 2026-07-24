package domain

import "time"

type PasswordResetToken struct {
	TokenHash string
	UserID    string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// PasswordResetEmail is persisted only as a retryable delivery record. Its
// body contains a short-lived reset URL, never a password or session secret.
type PasswordResetEmail struct {
	ID            int64
	UserID        string
	Recipient     string
	Subject       string
	Body          string
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LastErrorCode string
	CreatedAt     time.Time
	SentAt        time.Time
}
