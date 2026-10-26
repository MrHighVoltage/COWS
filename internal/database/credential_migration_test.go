package database

import (
	"context"
	"database/sql"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cows-project/cows/internal/domain"
)

func databaseBeforeCredentialMigration(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		"DROP INDEX users_email_unique",
		"ALTER TABLE users DROP COLUMN credential_version",
		"ALTER TABLE users DROP COLUMN email_key",
		"DELETE FROM schema_migrations WHERE version = 30",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func insertLegacyCredentialMail(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sessions (token_hash, user_id, created_at, expires_at, last_seen_at)
		VALUES ('legacy-session', 'first', 100, 9999999999, 100)`); err != nil {
		t.Fatal(err)
	}
	for _, purpose := range []string{domain.TokenPurposeReset, domain.TokenPurposeInvitation} {
		if _, err := db.Exec(`INSERT INTO password_reset_tokens (token_hash, user_id, purpose, expires_at, created_at)
			VALUES (?, 'first', ?, 9999999999, 100)`, purpose, purpose); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{domain.EmailKindPasswordReset, domain.EmailKindInvitation, domain.EmailKindTimeoutStop, domain.EmailKindWorkspaceDeleted} {
		if _, err := db.Exec(`INSERT INTO email_messages (kind, user_id, recipient, subject, body, status, next_attempt_at, created_at)
			VALUES (?, 'first', 'former@example.test', 'subject', 'body', 'pending', 100, 100)`, kind); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCredentialMigrationRefusesDuplicateEmailsWithoutChangingAccounts(t *testing.T) {
	for _, addresses := range []struct{ name, first, second string }{
		{"ASCII", "shared@example.test", "SHARED@example.test"},
		{"Unicode", "person@Ä.example", "person@ä.example"},
	} {
		t.Run(addresses.name, func(t *testing.T) {
			ctx := context.Background()
			db := databaseBeforeCredentialMigration(t)
			if _, err := db.ExecContext(ctx, `INSERT INTO users (id, username, email, display_name, password_hash, role, created_at, updated_at)
				VALUES ('first', 'first', ?, 'First', 'first-hash', 'user', 100, 100),
				('second', 'second', ?, 'Second', 'second-hash', 'administrator', 100, 100)`, addresses.first, addresses.second); err != nil {
				t.Fatal(err)
			}
			insertLegacyCredentialMail(t, db)
			if err := migrate(ctx, db); err == nil || !strings.Contains(err.Error(), "migration 30") {
				t.Fatalf("duplicate-address migration = %v, want explicit migration failure", err)
			}
			var count int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE (id = 'first' AND email = ? AND password_hash = 'first-hash') OR (id = 'second' AND email = ? AND password_hash = 'second-hash')", addresses.first, addresses.second).Scan(&count); err != nil || count != 2 {
				t.Fatalf("accounts after refused migration: count=%d err=%v", count, err)
			}
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = 30").Scan(&count); err != nil || count != 0 {
				t.Fatalf("migration was marked applied: count=%d err=%v", count, err)
			}
			for _, column := range []string{"credential_version", "email_key"} {
				if _, err := db.ExecContext(ctx, "SELECT "+column+" FROM users"); err == nil {
					t.Fatalf("failed migration left its %s column behind", column)
				}
			}
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM password_reset_tokens").Scan(&count); err != nil || count != 2 {
				t.Fatalf("failed migration retired tokens: count=%d err=%v", count, err)
			}
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions").Scan(&count); err != nil || count != 1 {
				t.Fatalf("failed migration retired sessions: count=%d err=%v", count, err)
			}
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM email_messages WHERE status = 'pending'").Scan(&count); err != nil || count != 4 {
				t.Fatalf("failed migration changed mail: count=%d err=%v", count, err)
			}
			if _, err := db.ExecContext(ctx, "UPDATE users SET email = 'corrected@example.test' WHERE id = 'second'"); err != nil {
				t.Fatal(err)
			}
			if err := migrate(ctx, db); err != nil {
				t.Fatalf("migration after explicit account correction: %v", err)
			}
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil || count != 2 {
				t.Fatalf("accounts after successful migration: count=%d err=%v", count, err)
			}
		})
	}
}

func TestCredentialMigrationPreservesDisplayEmailAndRetiresOldLinks(t *testing.T) {
	ctx := context.Background()
	db := databaseBeforeCredentialMigration(t)
	const address = "Person@Ä.Example"
	if _, err := db.ExecContext(ctx, `INSERT INTO users (id, username, email, display_name, password_hash, role, created_at, updated_at)
		VALUES ('first', 'first', ?, 'First', 'first-hash', 'user', 100, 100)`, address); err != nil {
		t.Fatal(err)
	}
	insertLegacyCredentialMail(t, db)
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var email string
	if err := db.QueryRowContext(ctx, "SELECT email FROM users WHERE email_key = ?", domain.EmailKey(" person@ä.example ")).Scan(&email); err != nil || email != address {
		t.Fatalf("migrated display address = %q, %v", email, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM password_reset_tokens").Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy tokens survived: count=%d err=%v", count, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy sessions survived: count=%d err=%v", count, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM email_messages WHERE status = 'canceled' AND kind IN ('password_reset', 'invitation')").Scan(&count); err != nil || count != 2 {
		t.Fatalf("legacy credential mail not canceled: count=%d err=%v", count, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM email_messages WHERE status = 'pending' AND kind IN ('timeout_stop', 'workspace_deleted')").Scan(&count); err != nil || count != 2 {
		t.Fatalf("non-credential mail changed: count=%d err=%v", count, err)
	}
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		t.Fatalf("SQLite maintenance requires no custom collation: %v", err)
	}
	if executable, err := exec.LookPath("sqlite3"); err == nil {
		var sequence int
		var name, path string
		if err := db.QueryRowContext(ctx, "PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
			t.Fatal(err)
		}
		output, err := exec.CommandContext(ctx, executable, path, "VACUUM; PRAGMA integrity_check;").CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != "ok" {
			t.Fatalf("offline sqlite3 maintenance: %v, %s", err, output)
		}
	}
}
