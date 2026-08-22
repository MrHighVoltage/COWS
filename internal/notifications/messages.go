package notifications

import (
	"fmt"
	"strings"
	"time"
)

// TimeoutWarningMessage warns about an upcoming automatic stop or deletion.
func TimeoutWarningMessage(workspaceName, action string, deadline time.Time) (string, string) {
	return "COWS workspace lifecycle warning", fmt.Sprintf(
		"Your COWS workspace %q is scheduled to be %s automatically at %s UTC.\n\nThis is an advisory notification; the workspace policy remains authoritative.",
		workspaceName, action, deadline.UTC().Format("2006-01-02 15:04"))
}

// PasswordResetMessage carries a short-lived reset URL and nothing else.
func PasswordResetMessage(resetURL string, expiresAt time.Time) (string, string) {
	return "COWS password reset", fmt.Sprintf(
		"A COWS password reset was requested for your account. Open this link before %s UTC to choose a new password:\n\n%s\n\nIf you did not request this, ignore this message.",
		expiresAt.UTC().Format("2006-01-02 15:04"), resetURL)
}

// InvitationMessage carries a single-use link that sets the first password.
func InvitationMessage(displayName, acceptURL string, expiresAt time.Time) (string, string) {
	return "Your COWS account", fmt.Sprintf(
		"An account was created for you on COWS%s.\n\nOpen this link before %s UTC to choose your password and sign in:\n\n%s\n\nThe link can be used once. If it expires, ask an administrator to send a new one.",
		greetingSuffix(displayName), expiresAt.UTC().Format("2006-01-02 15:04"), acceptURL)
}

// WelcomeMessage confirms a self-registered account. It is informational and
// carries no token: the user chose their own password during registration, and
// COWS deliberately does not verify addresses (decision 0015).
func WelcomeMessage(displayName, baseURL string) (string, string) {
	return "Welcome to COWS", fmt.Sprintf(
		"Your COWS account is ready%s. Sign in at:\n\n%s\n\nIf you did not create this account, contact your administrator.",
		greetingSuffix(displayName), strings.TrimRight(baseURL, "/")+"/login")
}

func greetingSuffix(displayName string) string {
	if strings.TrimSpace(displayName) == "" {
		return ""
	}
	return ", " + strings.TrimSpace(displayName)
}
