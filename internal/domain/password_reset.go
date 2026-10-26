package domain

import "time"

// Token purposes. A token issued for one purpose is never accepted by the
// other's consumption path.
const (
	TokenPurposeReset      = "reset"
	TokenPurposeInvitation = "invitation"
)

type PasswordResetToken struct {
	TokenHash         string
	UserID            string
	Purpose           string
	ExpiresAt         time.Time
	CreatedAt         time.Time
	CredentialVersion int64
	Recipient         string
	MinInterval       time.Duration
}
