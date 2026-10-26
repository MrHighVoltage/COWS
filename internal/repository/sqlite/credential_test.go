package sqlite

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
)

func credentialStoreFixture(t *testing.T, store *Store) (domain.User, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	user := domain.User{ID: "credential-user", Username: "credential-user", Email: "owner@example.test", Role: domain.RoleUser, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateUser(ctx, user, "original-hash"); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{"caller", "other"} {
		if err := store.CreateSession(ctx, domain.Session{TokenHash: hash, UserID: user.ID, CreatedAt: now, LastSeen: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreatePasswordResetToken(ctx, domain.PasswordResetToken{TokenHash: "reset-hash", UserID: user.ID, Purpose: domain.TokenPurposeReset, Recipient: user.Email, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertEmailMessage(ctx, domain.EmailMessage{Kind: domain.EmailKindPasswordReset, UserID: user.ID, Recipient: user.Email,
		CredentialTokenHash: "reset-hash", Subject: "reset", Body: "link", CreatedAt: now, NextAttemptAt: now}); err != nil {
		t.Fatal(err)
	}
	return user, now
}

func TestPasswordReplacementRollsBackEveryEffectWhenRevocationFails(t *testing.T) {
	for _, step := range []struct{ table, action string }{
		{"sessions", "DELETE"}, {"password_reset_tokens", "DELETE"}, {"email_messages", "UPDATE"},
	} {
		t.Run(step.table, func(t *testing.T) {
			ctx := context.Background()
			store := outboxTestStore(t)
			user, now := credentialStoreFixture(t, store)
			if _, err := store.db.ExecContext(ctx, fmt.Sprintf("CREATE TRIGGER reject_revocation BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'injected revocation failure'); END", step.action, step.table)); err != nil {
				t.Fatal(err)
			}
			if err := store.ReplaceUserPassword(ctx, user.ID, "replacement-hash", false, user.CredentialVersion, "caller"); err == nil {
				t.Fatal("expected revocation failure")
			}
			record, err := store.FindUserCredentialsByID(ctx, user.ID)
			if err != nil || record.PasswordHash != "original-hash" || record.User.CredentialVersion != 0 {
				t.Fatalf("password replacement partially committed: %+v, %v", record, err)
			}
			for _, hash := range []string{"caller", "other"} {
				if _, err := store.FindSessionUser(ctx, hash, now.Unix()); err != nil {
					t.Fatalf("session %s lost despite rollback: %v", hash, err)
				}
			}
			var count int
			if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM password_reset_tokens WHERE token_hash = 'reset-hash'").Scan(&count); err != nil || count != 1 {
				t.Fatalf("tokens after rollback: %d, %v", count, err)
			}
			pending, err := store.ListPendingEmailMessages(ctx, now, 10)
			if err != nil || len(pending) != 1 {
				t.Fatalf("queued mail after rollback: %+v, %v", pending, err)
			}
		})
	}
}

func TestPasswordReplacementKeepsOnlyCallerAndRejectsStaleProof(t *testing.T) {
	ctx := context.Background()
	store := outboxTestStore(t)
	user, now := credentialStoreFixture(t, store)
	if err := store.ReplaceUserPassword(ctx, user.ID, "replacement-hash", false, user.CredentialVersion, "caller"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindSessionUser(ctx, "caller", now.Unix()); err != nil {
		t.Fatalf("caller session lost: %v", err)
	}
	if _, err := store.FindSessionUser(ctx, "other", now.Unix()); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("other session survived: %v", err)
	}
	if err := store.ReplaceUserPassword(ctx, user.ID, "stale-hash", false, user.CredentialVersion, "caller"); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("earlier credential proof accepted: %v", err)
	}
	if _, err := store.ResetPasswordUsingToken(ctx, "reset-hash", domain.TokenPurposeReset, "token-hash", now); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("old credential token survived: %v", err)
	}
	pending, err := store.ListPendingEmailMessages(ctx, now, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("credential mail survived: %+v, %v", pending, err)
	}
}

func TestCredentialMailCannotBeQueuedAfterItsTokenIsRetired(t *testing.T) {
	for _, mutation := range []string{"email correction", "password replacement", "same-purpose supersession"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			store := outboxTestStore(t)
			user, now := credentialStoreFixture(t, store)
			var err error
			switch mutation {
			case "email correction":
				err = store.UpdateUserProfile(ctx, user.ID, "corrected@example.test", "", now)
			case "password replacement":
				err = store.ReplaceUserPassword(ctx, user.ID, "replacement-hash", false, 0, "")
			case "same-purpose supersession":
				err = store.CreatePasswordResetToken(ctx, domain.PasswordResetToken{TokenHash: "replacement-token", UserID: user.ID, Purpose: domain.TokenPurposeReset, Recipient: user.Email, CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := store.UpsertEmailMessage(ctx, domain.EmailMessage{Kind: domain.EmailKindPasswordReset, UserID: user.ID, Recipient: user.Email,
				CredentialTokenHash: "reset-hash", Subject: "delayed reset", Body: "old link", CreatedAt: now, NextAttemptAt: now}); err != nil {
				t.Fatal(err)
			}
			pending, err := store.ListPendingEmailMessages(ctx, now, 10)
			if err != nil || len(pending) != 0 {
				t.Fatalf("superseded credential mail queued: %+v, %v", pending, err)
			}
		})
	}
}

func TestImportRollsBackAddressAndRevocationTogether(t *testing.T) {
	ctx := context.Background()
	store := outboxTestStore(t)
	user, now := credentialStoreFixture(t, store)
	updated := user
	updated.Email = "corrected@example.test"
	// A foreign-key error after the address update must roll back token and
	// outbox invalidation along with the import's other changes.
	if err := store.ImportUsers(ctx, []repository.UserImportEntry{{User: updated, Existing: true, GroupIDs: []string{"missing-group"}}}); err == nil {
		t.Fatal("expected import failure")
	}
	current, err := store.FindUserByID(ctx, user.ID)
	if err != nil || current.Email != user.Email || current.CredentialVersion != user.CredentialVersion {
		t.Fatalf("failed import changed account: %+v, %v", current, err)
	}
	if _, err := store.ResetPasswordUsingToken(ctx, "reset-hash", domain.TokenPurposeReset, "replacement-hash", now); err != nil {
		t.Fatalf("failed import retired original token: %v", err)
	}
}

func TestEmailIndexCoversUpdatesImportsAndBlankAddresses(t *testing.T) {
	for _, addresses := range []struct{ name, first, duplicate string }{
		{"ASCII", "shared@example.test", " SHARED@example.test "},
		{"Unicode", "Person@Ä.Example", " person@ä.example "},
	} {
		t.Run(addresses.name, func(t *testing.T) {
			ctx := context.Background()
			store := outboxTestStore(t)
			now := time.Now().UTC()
			for _, user := range []domain.User{
				{ID: "first", Username: "first", Email: addresses.first, Role: domain.RoleUser, CreatedAt: now, UpdatedAt: now},
				{ID: "second", Username: "second", Role: domain.RoleUser, CreatedAt: now, UpdatedAt: now},
				{ID: "third", Username: "third", Role: domain.RoleUser, CreatedAt: now, UpdatedAt: now},
			} {
				if err := store.CreateUser(ctx, user, "hash"); err != nil {
					t.Fatal(err)
				}
			}
			if record, err := store.FindUserByEmail(ctx, addresses.duplicate); err != nil || record.User.ID != "first" || record.User.Email != addresses.first {
				t.Fatalf("canonical lookup changed display email: %+v, %v", record, err)
			}
			if err := store.CreateUser(ctx, domain.User{ID: "fourth", Username: "fourth", Email: addresses.duplicate, Role: domain.RoleUser, CreatedAt: now, UpdatedAt: now}, "hash"); !errors.Is(err, repository.ErrEmailInUse) {
				t.Fatalf("duplicate creation = %v", err)
			}
			if err := store.UpdateUserProfile(ctx, "second", addresses.duplicate, "", now); !errors.Is(err, repository.ErrEmailInUse) {
				t.Fatalf("duplicate update = %v", err)
			}
			if err := store.ImportUsers(ctx, []repository.UserImportEntry{{User: domain.User{ID: "second", Email: addresses.duplicate, UpdatedAt: now}, Existing: true}}); !errors.Is(err, repository.ErrEmailInUse) {
				t.Fatalf("duplicate import = %v", err)
			}
		})
	}
}
