package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/database"
	"github.com/cows-project/cows/internal/domain"
)

func outboxTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db)
}

func TestDedupeKeyCollapsesMailWhileOneShotMailDoesNot(t *testing.T) {
	ctx := context.Background()
	store := outboxTestStore(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	for _, body := range []string{"first", "first"} {
		if err := store.UpsertEmailMessage(ctx, domain.EmailMessage{
			Kind: domain.EmailKindTimeoutDelete, UserID: "owner-1", Recipient: "owner@example.test",
			Subject: "warning", Body: body, DedupeKey: "workspace:w1:timeout_delete",
			Status: "pending", NextAttemptAt: now, CreatedAt: now,
		}); err != nil {
			t.Fatalf("upsert deduplicated message: %v", err)
		}
	}
	// A NULL dedupe key must never collide, however many are queued.
	for range 2 {
		if err := store.UpsertEmailMessage(ctx, domain.EmailMessage{
			Kind: domain.EmailKindPasswordReset, UserID: "owner-1", Recipient: "owner@example.test",
			Subject: "reset", Body: "link", Status: "pending", NextAttemptAt: now, CreatedAt: now,
		}); err != nil {
			t.Fatalf("upsert reset message: %v", err)
		}
	}

	pending, err := store.ListPendingEmailMessages(ctx, now, 50)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("expected 3 pending messages, got %d", len(pending))
	}
}

func TestASentWarningIsRequeuedOnlyWhenItSaysSomethingNew(t *testing.T) {
	ctx := context.Background()
	store := outboxTestStore(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	message := domain.EmailMessage{
		Kind: domain.EmailKindTimeoutStop, UserID: "owner-1", Recipient: "owner@example.test",
		Subject: "warning", Body: "stopping at 13:00", DedupeKey: "workspace:w1:timeout_stop",
		Status: "pending", NextAttemptAt: now, CreatedAt: now,
	}
	if err := store.UpsertEmailMessage(ctx, message); err != nil {
		t.Fatalf("queue warning: %v", err)
	}
	pending, err := store.ListPendingEmailMessages(ctx, now, 50)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if err := store.MarkEmailMessageSent(ctx, pending[0].ID, now); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	// Re-enqueueing the identical warning must not send it a second time.
	if err := store.UpsertEmailMessage(ctx, message); err != nil {
		t.Fatalf("requeue identical warning: %v", err)
	}
	if pending, err = store.ListPendingEmailMessages(ctx, now, 50); err != nil {
		t.Fatalf("list pending: %v", err)
	} else if len(pending) != 0 {
		t.Fatalf("identical warning was requeued: %+v", pending)
	}
	// A moved deadline changes the body, which is a different statement and
	// must be delivered.
	message.Body = "stopping at 15:00"
	if err := store.UpsertEmailMessage(ctx, message); err != nil {
		t.Fatalf("requeue moved warning: %v", err)
	}
	if pending, err = store.ListPendingEmailMessages(ctx, now, 50); err != nil {
		t.Fatalf("list pending: %v", err)
	} else if len(pending) != 1 {
		t.Fatalf("a moved deadline must requeue the warning, got %d pending", len(pending))
	}
}

func TestCancellingWorkspaceMailIsScopedToThatWorkspace(t *testing.T) {
	ctx := context.Background()
	store := outboxTestStore(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	for _, workspaceID := range []string{"w1", "w2"} {
		if err := store.UpsertEmailMessage(ctx, domain.EmailMessage{
			Kind: domain.EmailKindTimeoutStop, UserID: "owner-1", Recipient: "owner@example.test",
			Subject: "warning", Body: workspaceID, DedupeKey: "workspace:" + workspaceID + ":timeout_stop",
			Status: "pending", NextAttemptAt: now, CreatedAt: now,
		}); err != nil {
			t.Fatalf("queue warning for %s: %v", workspaceID, err)
		}
	}
	if err := store.CancelEmailMessagesForWorkspace(ctx, "w1"); err != nil {
		t.Fatalf("cancel workspace mail: %v", err)
	}
	pending, err := store.ListPendingEmailMessages(ctx, now, 50)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 || pending[0].DedupeKey != "workspace:w2:timeout_stop" {
		t.Fatalf("cancellation was not scoped to one workspace: %+v", pending)
	}
}

func TestCountRecentEmailMessagesBacksThrottling(t *testing.T) {
	ctx := context.Background()
	store := outboxTestStore(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertEmailMessage(ctx, domain.EmailMessage{
		Kind: domain.EmailKindPasswordReset, UserID: "owner-1", Recipient: "owner@example.test",
		Subject: "reset", Body: "link", Status: "pending", NextAttemptAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("queue reset: %v", err)
	}
	count, err := store.CountRecentEmailMessages(ctx, "owner-1", domain.EmailKindPasswordReset, now.Add(-5*time.Minute))
	if err != nil {
		t.Fatalf("count recent: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 recent reset, got %d", count)
	}
	if count, err = store.CountRecentEmailMessages(ctx, "owner-1", domain.EmailKindPasswordReset, now.Add(time.Minute)); err != nil {
		t.Fatalf("count recent after the window: %v", err)
	} else if count != 0 {
		t.Fatalf("expected the older message to fall outside the window, got %d", count)
	}
}

// An address correction has to take with it both the links mailed to the old
// address and any message still queued for it, while leaving mail that carries
// no token alone.
func TestChangingAnEmailRetiresItsCredentialLinksAndQueuedMail(t *testing.T) {
	ctx := context.Background()
	store := outboxTestStore(t)
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	user := domain.User{ID: "user-1", Username: "student", Email: "typo@example.test", DisplayName: "Student", Role: domain.RoleUser, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateUser(ctx, user, "hash"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	for _, purpose := range []string{domain.TokenPurposeReset, domain.TokenPurposeInvitation} {
		if err := store.CreatePasswordResetToken(ctx, domain.PasswordResetToken{
			TokenHash: "hash-" + purpose, UserID: user.ID, Purpose: purpose,
			ExpiresAt: now.Add(time.Hour), CreatedAt: now,
		}); err != nil {
			t.Fatalf("create %s token: %v", purpose, err)
		}
	}
	for _, kind := range []string{domain.EmailKindInvitation, domain.EmailKindPasswordReset, domain.EmailKindWorkspaceDeleted} {
		if err := store.UpsertEmailMessage(ctx, domain.EmailMessage{
			Kind: kind, UserID: user.ID, Recipient: user.Email, Subject: kind, Body: "body",
			Status: "pending", NextAttemptAt: now, CreatedAt: now,
		}); err != nil {
			t.Fatalf("queue %s message: %v", kind, err)
		}
	}

	if err := store.UpdateUserProfile(ctx, user.ID, "student@example.test", "Student", now); err != nil {
		t.Fatalf("update profile: %v", err)
	}

	var tokens int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM password_reset_tokens WHERE user_id = ?", user.ID).Scan(&tokens); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if tokens != 0 {
		t.Fatalf("tokens surviving an address change = %d, want 0", tokens)
	}
	pending, err := store.ListPendingEmailMessages(ctx, now, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 || pending[0].Kind != domain.EmailKindWorkspaceDeleted {
		t.Fatalf("pending after an address change = %+v, want only the workspace notice", pending)
	}

	// Rewriting the same address is not a change and retires nothing.
	if err := store.CreatePasswordResetToken(ctx, domain.PasswordResetToken{
		TokenHash: "hash-kept", UserID: user.ID, Purpose: domain.TokenPurposeReset,
		ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}); err != nil {
		t.Fatalf("create token: %v", err)
	}
	if err := store.UpdateUserProfile(ctx, user.ID, "student@example.test", "Real Student", now); err != nil {
		t.Fatalf("update display name only: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM password_reset_tokens WHERE user_id = ?", user.ID).Scan(&tokens); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if tokens != 1 {
		t.Fatalf("tokens after a display-name-only edit = %d, want 1", tokens)
	}
}
