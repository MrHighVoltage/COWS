# Adaptive Lifecycle Warnings and Account Invitations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the fixed timeout-warning lead time with a window-relative one, notify owners when a workspace is actually deleted, unify the two email outboxes into one, add account invitations that force an initial password choice, and refine password reset.

**Architecture:** One `email_messages` table replaces `email_notifications` and `password_reset_emails`, with a nullable `dedupe_key` under a partial unique index. `internal/notifications` gains a pure lead-time function and collapses to a single delivery loop over one `Send(ctx, recipient, subject, body)` interface. `password_reset_tokens` gains a `purpose` column so invitations reuse the hashed single-use token machinery. Enqueueing is best-effort everywhere: no lifecycle operation, user creation, or deletion is rolled back because mail could not be queued.

**Tech Stack:** Go standard library, SQLite with application-controlled migrations in `internal/database/migrations`, `html/template` server-rendered pages, HTMX. No new third-party dependency.

## Global Constraints

- Source spec: `docs/superpowers/specs/2026-08-22-email-notifications-design.md`. Read it before starting.
- Project rules: `AGENTS.md`. Read it before starting. Uppercase `COWS` everywhere.
- Prefer the Go standard library. Do **not** add any dependency in this work.
- Podman must not be required by any test added here. Tests use a real SQLite store opened on `t.TempDir()`, following the existing pattern in `internal/notifications/service_test.go`.
- Never log, render, or persist SMTP credentials, raw tokens, passwords, host paths, volume names, runtime identifiers, or user content.
- Never render raw runtime, filesystem, or database errors to ordinary users.
- Migrations are additive files in `internal/database/migrations/`, applied in filename order. Never edit an existing migration file.
- Every task ends with a green `go test ./...`, `go vet ./...`, and `gofmt -l` returning nothing.
- Use table-driven tests. Existing tests that this work invalidates must be **updated**, not deleted.
- Commit after every task with a descriptive message. Do not amend earlier commits.

**Verification command run at the end of every task:**

```bash
gofmt -l $(git ls-files '*.go') && go vet ./... && go test ./...
```

Expected: no output from `gofmt -l`, no vet findings, all tests pass.

---

## File Structure

**Created:**
- `internal/database/migrations/0028_unified_email_outbox.sql` — merge both outbox tables
- `internal/database/migrations/0029_reset_token_purpose.sql` — token purpose column
- `internal/domain/email.go` — `EmailMessage` and the message-kind constants
- `internal/notifications/lead.go` — pure lead-time computation
- `internal/notifications/lead_test.go`
- `internal/notifications/messages.go` — subject/body builders per kind
- `internal/auth/invitation.go` — invitation issue and accept
- `internal/auth/invitation_test.go`
- `web/templates/pages/invitation-accept.html`
- `docs/decisions/0027-adaptive-lifecycle-warnings-and-account-invitations.md`

**Modified:**
- `internal/domain/notification.go` — remove `EmailNotification`
- `internal/domain/password_reset.go` — remove `PasswordResetEmail`, add `Purpose` to `PasswordResetToken`
- `internal/repository/repository.go` — replace two outbox interfaces with `EmailOutboxRepository`
- `internal/repository/sqlite/store.go` — outbox implementation, token purpose
- `internal/notifications/service.go` — single delivery loop, adaptive lead, new enqueue methods
- `internal/notifications/smtp.go` — single `Send` signature
- `internal/config/config.go` — new keys, removed key
- `internal/auth/service.go` — reset lifetime, throttle, unusable hash
- `internal/auth/import.go` — invitations for imported users
- `internal/web/server.go` — invitation routes and handlers, admin reset action
- `internal/workspace/workspace.go` — deletion notices
- `web/templates/pages/admin-users-new.html`, `admin-user-edit.html`
- `cmd/cows/main.go` — wiring
- `AGENTS.md`, `ARCHITECTURE.md`, `SECURITY.md`, `ROADMAP.md`, `deploy/` config docs

---

## Task 1: Unified email outbox schema and repository

Replaces `email_notifications` and `password_reset_emails` with one table. No behavior changes yet — this task lands the storage layer and its tests while the old code still compiles against the new methods.

**Files:**
- Create: `internal/database/migrations/0028_unified_email_outbox.sql`
- Create: `internal/domain/email.go`
- Modify: `internal/domain/notification.go`, `internal/domain/password_reset.go`
- Modify: `internal/repository/repository.go:75-90`
- Modify: `internal/repository/sqlite/store.go`
- Test: `internal/repository/sqlite/store_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `domain.EmailMessage`, the `domain.EmailKind*` constants, and `repository.EmailOutboxRepository` with the exact signatures below. Every later task uses these.

- [ ] **Step 1: Write the failing migration test**

Add to `internal/repository/sqlite/store_test.go`:

```go
func TestUnifiedOutboxMigratesExistingRows(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := New(db)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	if err := store.UpsertEmailMessage(ctx, domain.EmailMessage{
		Kind: domain.EmailKindTimeoutDelete, UserID: "owner-1", Recipient: "owner@example.test",
		Subject: "s", Body: "b", DedupeKey: "workspace:w1:timeout_delete",
		Status: "pending", NextAttemptAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("upsert first message: %v", err)
	}
	// The same dedupe key must update in place rather than queue a second mail.
	if err := store.UpsertEmailMessage(ctx, domain.EmailMessage{
		Kind: domain.EmailKindTimeoutDelete, UserID: "owner-1", Recipient: "owner@example.test",
		Subject: "s2", Body: "b2", DedupeKey: "workspace:w1:timeout_delete",
		Status: "pending", NextAttemptAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("upsert duplicate message: %v", err)
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/repository/sqlite/ -run TestUnifiedOutboxMigratesExistingRows -v`
Expected: FAIL — `store.UpsertEmailMessage undefined` and `domain.EmailMessage undefined`.

- [ ] **Step 3: Write the migration**

Create `internal/database/migrations/0028_unified_email_outbox.sql`:

```sql
CREATE TABLE email_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,
    user_id TEXT,
    recipient TEXT NOT NULL,
    subject TEXT NOT NULL,
    body TEXT NOT NULL,
    dedupe_key TEXT,
    status TEXT NOT NULL CHECK (status IN ('pending', 'sent', 'canceled')),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    last_error_code TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    sent_at INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX email_messages_dedupe_idx
    ON email_messages(dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX email_messages_pending_idx
    ON email_messages(status, next_attempt_at);
CREATE INDEX email_messages_user_idx
    ON email_messages(user_id, kind, created_at);

INSERT INTO email_messages
    (kind, user_id, recipient, subject, body, dedupe_key, status,
     attempts, next_attempt_at, last_error_code, created_at, sent_at)
SELECT kind, owner_user_id, recipient, subject, body,
       'workspace:' || workspace_id || ':' || kind, status,
       attempts, next_attempt_at, last_error_code, created_at, sent_at
FROM email_notifications;

INSERT INTO email_messages
    (kind, user_id, recipient, subject, body, dedupe_key, status,
     attempts, next_attempt_at, last_error_code, created_at, sent_at)
SELECT 'password_reset', user_id, recipient, subject, body, NULL, status,
       attempts, next_attempt_at, last_error_code, created_at,
       COALESCE(sent_at, 0)
FROM password_reset_emails;

DROP TABLE email_notifications;
DROP TABLE password_reset_emails;
```

`user_id` is deliberately not a foreign key: a deletion notice must survive the user row, where the old `ON DELETE CASCADE` on `password_reset_emails` would have discarded queued mail.

- [ ] **Step 4: Write the domain type**

Create `internal/domain/email.go`:

```go
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
	// DedupeKey is empty for one-shot mail. A non-empty value is unique
	// across the table, so re-enqueueing updates in place instead of
	// queueing a second copy.
	DedupeKey     string
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LastErrorCode string
	CreatedAt     time.Time
	SentAt        time.Time
}
```

Delete `EmailNotification` and the two `Notification*` constants from `internal/domain/notification.go` (delete the file if nothing else remains in it), and delete `PasswordResetEmail` from `internal/domain/password_reset.go`.

- [ ] **Step 5: Replace the repository interfaces**

In `internal/repository/repository.go`, delete `PasswordResetEmailRepository` and `NotificationRepository` and add:

```go
type EmailOutboxRepository interface {
	// UpsertEmailMessage queues a message. A non-empty DedupeKey updates any
	// existing row with that key instead of inserting a second one.
	UpsertEmailMessage(ctx context.Context, message domain.EmailMessage) error
	ListPendingEmailMessages(ctx context.Context, now time.Time, limit int) ([]domain.EmailMessage, error)
	MarkEmailMessageSent(ctx context.Context, id int64, sentAt time.Time) error
	MarkEmailMessageFailed(ctx context.Context, id int64, attempts int, nextAttemptAt time.Time, errorCode string) error
	MarkEmailMessageCanceled(ctx context.Context, id int64) error
	CancelEmailMessagesForWorkspace(ctx context.Context, workspaceID string) error
	CancelEmailMessagesForUser(ctx context.Context, userID string) error
	// CountRecentEmailMessages counts messages of one kind queued for a user
	// at or after `since`, regardless of status. It backs reset throttling.
	CountRecentEmailMessages(ctx context.Context, userID, kind string, since time.Time) (int, error)
}
```

Replace the two removed names in the `Store` interface composition list (around `internal/repository/repository.go:193`) with `EmailOutboxRepository`.

- [ ] **Step 6: Implement the store methods**

In `internal/repository/sqlite/store.go`, replace the `*EmailNotification*` and `*PasswordResetEmail*` methods with implementations of the seven methods above, following the surrounding code's style: `BeginTx`/`defer tx.Rollback()` for writes, `repository.ErrNotFound` when `RowsAffected() == 0`, Unix seconds for times.

`UpsertEmailMessage` inserts with an upsert that leaves an already-`sent` row alone:

```go
func (s *Store) UpsertEmailMessage(ctx context.Context, message domain.EmailMessage) error {
	var dedupe any
	if message.DedupeKey != "" {
		dedupe = message.DedupeKey
	}
	var userID any
	if message.UserID != "" {
		userID = message.UserID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO email_messages
		(kind, user_id, recipient, subject, body, dedupe_key, status, attempts, next_attempt_at, last_error_code, created_at, sent_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, '', ?, 0)
		ON CONFLICT(dedupe_key) DO UPDATE SET
			recipient = excluded.recipient,
			subject = excluded.subject,
			body = excluded.body,
			next_attempt_at = excluded.next_attempt_at
		WHERE email_messages.status = 'pending'`,
		message.Kind, userID, message.Recipient, message.Subject, message.Body,
		dedupe, message.Status, message.NextAttemptAt.Unix(), message.CreatedAt.Unix())
	if err != nil {
		return fmt.Errorf("queue email message: %w", err)
	}
	return nil
}
```

`CancelEmailMessagesForWorkspace` matches on the dedupe-key prefix, since `workspace_id` is no longer a column:

```go
func (s *Store) CancelEmailMessagesForWorkspace(ctx context.Context, workspaceID string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE email_messages SET status = 'canceled' WHERE status = 'pending' AND dedupe_key LIKE ? ESCAPE '\\'",
		escapeLike("workspace:"+workspaceID+":")+"%")
	if err != nil {
		return fmt.Errorf("cancel workspace email messages: %w", err)
	}
	return nil
}
```

Add the helper if the file does not already have one:

```go
// escapeLike makes a value safe as a LIKE prefix. Workspace IDs are generated,
// but a wildcard reaching this query would cancel unrelated mail.
func escapeLike(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return replacer.Replace(value)
}
```

`CountRecentEmailMessages`:

```go
func (s *Store) CountRecentEmailMessages(ctx context.Context, userID, kind string, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM email_messages WHERE user_id = ? AND kind = ? AND created_at >= ?",
		userID, kind, since.Unix()).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count recent email messages: %w", err)
	}
	return count, nil
}
```

`ListPendingEmailMessages` selects `WHERE status = 'pending' AND next_attempt_at <= ? ORDER BY next_attempt_at, id LIMIT ?`. The three `Mark*` methods mirror the old `MarkEmailNotification*` bodies against the new table.

- [ ] **Step 7: Update the call sites that no longer compile**

`internal/notifications/service.go` and `internal/workspace/workspace.go` reference the removed methods. Do the mechanical rename only — behavior changes belong to later tasks:
- `UpsertEmailNotification` / `UpsertPasswordResetEmail` → `UpsertEmailMessage`
- `ListPendingEmailNotifications` / `ListPendingPasswordResetEmails` → `ListPendingEmailMessages`
- `MarkEmailNotification*` / `MarkPasswordResetEmail*` → `MarkEmailMessage*`
- `CancelEmailNotificationsForWorkspace` → `CancelEmailMessagesForWorkspace` (two call sites in `internal/workspace/workspace.go`, near lines 847 and 919, plus one in the `TimeoutActionDelete` branch near line 1283)
- `CancelEmailNotificationsForUser` → `CancelEmailMessagesForUser`

`domain.EmailNotification` becomes `domain.EmailMessage`; set `DedupeKey` to `"workspace:"+value.ID+":"+kind` where warnings are enqueued, and carry `OwnerUserID` into `UserID`. The `Deadline` field is gone — Task 2 reintroduces the staleness check that needed it, so for now drop `notificationIsCurrent`'s deadline comparison and leave the rest of the check in place.

- [ ] **Step 8: Run the tests**

Run: `go test ./internal/repository/... ./internal/notifications/... ./internal/workspace/... -v`
Expected: PASS, including `TestUnifiedOutboxMigratesExistingRows`.

- [ ] **Step 9: Full verification and commit**

```bash
gofmt -l $(git ls-files '*.go') && go vet ./... && go test ./...
git add -A
git commit -m "Unify the email outbox into a single table"
```

---

## Task 2: Single delivery loop and per-kind staleness

Collapses `Deliver` to one loop over one sender interface, and restores the deadline-based staleness check on a per-kind basis.

**Files:**
- Modify: `internal/notifications/service.go`
- Modify: `internal/notifications/smtp.go:44-50`
- Create: `internal/notifications/messages.go`
- Modify: `internal/notifications/service_test.go`
- Modify: `cmd/cows/main.go:105-115`

**Interfaces:**
- Consumes: `domain.EmailMessage`, `repository.EmailOutboxRepository` from Task 1.
- Produces: `notifications.Sender` with `Send(ctx context.Context, recipient, subject, body string) error`; `Service.Deliver(ctx) error`; message builders `notifications.TimeoutWarningMessage`, `notifications.WorkspaceDeletedMessage`, `notifications.PasswordResetMessage`, `notifications.InvitationMessage`, `notifications.WelcomeMessage`.

- [ ] **Step 1: Write the failing test for a single loop**

Replace `fakeSender` in `internal/notifications/service_test.go` with:

```go
type sentMessage struct {
	recipient string
	subject   string
	body      string
}

type fakeSender struct {
	error error
	sent  []sentMessage
}

func (s *fakeSender) Send(_ context.Context, recipient, subject, body string) error {
	if s.error != nil {
		return s.error
	}
	s.sent = append(s.sent, sentMessage{recipient: recipient, subject: subject, body: body})
	return nil
}
```

Add:

```go
func TestDeliverSendsEveryKindThroughOneLoop(t *testing.T) {
	ctx := context.Background()
	store, base := notificationTestStore(t, domain.Workspace{
		ID: "workspace-1", OwnerUserID: "owner-1", TemplateID: "template-1", Name: "Research workspace",
		DesiredState: domain.DesiredWorkspaceRunning, ObservedState: string(runtime.StateRunning),
		RuntimeID: "runtime-1", InitialConnectionTimeoutSeconds: 3600,
		StartedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		IdleSince: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		CreatedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
	})
	sender := &fakeSender{}
	service, err := New(store, sender, LeadPolicy{Divisor: 3, Max: 24 * time.Hour, Min: time.Hour}, time.Minute)
	if err != nil {
		t.Fatalf("create notification service: %v", err)
	}
	service.now = func() time.Time { return base }

	for _, kind := range []string{domain.EmailKindPasswordReset, domain.EmailKindInvitation, domain.EmailKindWelcome} {
		if err := store.UpsertEmailMessage(ctx, domain.EmailMessage{
			Kind: kind, UserID: "owner-1", Recipient: "owner@example.test",
			Subject: kind, Body: "body", Status: "pending", NextAttemptAt: base, CreatedAt: base,
		}); err != nil {
			t.Fatalf("queue %s: %v", kind, err)
		}
	}
	if err := service.Deliver(ctx); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if len(sender.sent) != 3 {
		t.Fatalf("expected 3 delivered messages, got %d", len(sender.sent))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/notifications/ -run TestDeliverSendsEveryKindThroughOneLoop -v`
Expected: FAIL — `LeadPolicy undefined` and the `Sender` interface mismatch.

- [ ] **Step 3: Collapse the sender interface**

In `internal/notifications/service.go`, delete `MessageSender` and change `Sender` to:

```go
type Sender interface {
	Send(ctx context.Context, recipient, subject, body string) error
}
```

In `internal/notifications/smtp.go`, delete the `Send(ctx, notification)` and `SendMessage(...)` wrappers and rename `sendMessage` to `Send` with the exported signature. `SMTPSender` then satisfies `Sender` directly.

Delete the `messageSender` field from `Service` and the type assertion in `New`.

- [ ] **Step 4: Collapse Deliver to one loop**

Replace both loops in `Deliver` with:

```go
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
```

- [ ] **Step 5: Make staleness per-kind**

Rename `notificationIsCurrent` to `messageIsCurrent` and gate it on kind. Only the two warning kinds are re-checked; everything else is unconditional, because a reset or invitation link is valid whatever happened since:

```go
// messageIsCurrent re-checks the two lifecycle warnings against live state so
// a warning is not delivered after the deadline it describes has moved or the
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

// workspaceIDFromDedupeKey parses "workspace:<id>:<kind>". Workspace IDs are
// URL-safe base64 and never contain a colon.
func workspaceIDFromDedupeKey(key string) (string, bool) {
	parts := strings.Split(key, ":")
	if len(parts) != 3 || parts[0] != "workspace" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}
```

The old exact-deadline equality check is dropped: the deadline is no longer stored on the row, and phase plus observed state already establish that the warning still describes reality.

- [ ] **Step 6: Extract the message builders**

Create `internal/notifications/messages.go` holding one function per kind, each returning `(subject, body string)`. Move the two existing bodies verbatim from `service.go`:

```go
package notifications

import (
	"fmt"
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
```

`WorkspaceDeletedMessage`, `InvitationMessage`, and `WelcomeMessage` are added by Tasks 4, 6, and 9 respectively — do not stub them now.

Rewrite `EnqueueTimeoutWarnings` and `EnqueuePasswordReset` to call these builders.

- [ ] **Step 7: Update main.go wiring**

`cmd/cows/main.go:111` passes `cfg.EmailWarningLeadTime`. Task 3 introduces `LeadPolicy`; for now change the call to construct it inline so the tree compiles:

```go
notificationService, err = notifications.New(store, sender, notifications.LeadPolicy{Divisor: 3, Max: 24 * time.Hour, Min: time.Hour}, cfg.EmailRetryInterval)
```

and add the `LeadPolicy` type to `service.go` with a `Validate() error` method:

```go
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
```

Change `New` to take `LeadPolicy` instead of `warningLead time.Duration` and call `Validate()`.

- [ ] **Step 8: Fix the existing tests**

`TestTimeoutWarningsAreDeduplicatedAndDelivered` and `TestFailedEmailDeliveryStopsAfterBoundedAttempts` construct the service with a `time.Hour` lead. Change both to the `LeadPolicy{Divisor: 3, Max: 24 * time.Hour, Min: time.Hour}` form and change their assertions from `sender.sent[0].Subject` to `sender.sent[0].subject`.

**Note:** `TestTimeoutWarningsAreDeduplicatedAndDelivered` uses `InitialConnectionTimeoutSeconds: 3600`. Under Task 3's policy that window yields a 20-minute lead, below the one-hour floor, so no warning is enqueued at all. Change that workspace to `InitialConnectionTimeoutSeconds: 6 * 3600` and move the `service.now` offset to `base.Add(5 * time.Hour)`, giving a one-hour remaining window against a two-hour lead. Do this now so the test is already correct when Task 3 lands.

- [ ] **Step 9: Run the tests**

Run: `go test ./internal/notifications/ -v`
Expected: PASS, all three tests.

- [ ] **Step 10: Full verification and commit**

```bash
gofmt -l $(git ls-files '*.go') && go vet ./... && go test ./...
git add -A
git commit -m "Collapse email delivery into a single loop"
```

---

## Task 3: Adaptive warning lead time

**Files:**
- Create: `internal/notifications/lead.go`
- Create: `internal/notifications/lead_test.go`
- Modify: `internal/notifications/service.go` (`EnqueueTimeoutWarnings`)
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go:27, 48-64`
- Modify: `cmd/cows/main.go`

**Interfaces:**
- Consumes: `notifications.LeadPolicy` from Task 2.
- Produces: `func (p LeadPolicy) Lead(window time.Duration) (time.Duration, bool)` — the second return is `false` when no warning should be sent. `config.Config` fields `EmailWarningLeadDivisor int`, `EmailWarningLeadMax time.Duration`, `EmailWarningLeadMin time.Duration`.

- [ ] **Step 1: Write the failing test**

Create `internal/notifications/lead_test.go`:

```go
package notifications

import (
	"testing"
	"time"
)

func TestLeadIsProportionalAndBounded(t *testing.T) {
	policy := LeadPolicy{Divisor: 3, Max: 24 * time.Hour, Min: time.Hour}
	cases := []struct {
		name   string
		window time.Duration
		lead   time.Duration
		send   bool
	}{
		{name: "one hour stop window is below the floor", window: time.Hour, send: false},
		{name: "three hour window sits exactly on the floor", window: 3 * time.Hour, lead: time.Hour, send: true},
		{name: "just under the floor is suppressed", window: 3*time.Hour - 3*time.Second, send: false},
		{name: "six hour window", window: 6 * time.Hour, lead: 2 * time.Hour, send: true},
		{name: "two day window", window: 48 * time.Hour, lead: 16 * time.Hour, send: true},
		{name: "three day window is the crossover", window: 72 * time.Hour, lead: 24 * time.Hour, send: true},
		{name: "thirty day window is capped", window: 720 * time.Hour, lead: 24 * time.Hour, send: true},
		{name: "zero window sends nothing", window: 0, send: false},
		{name: "negative window sends nothing", window: -time.Hour, send: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			lead, send := policy.Lead(testCase.window)
			if send != testCase.send {
				t.Fatalf("expected send=%v, got %v", testCase.send, send)
			}
			if send && lead != testCase.lead {
				t.Fatalf("expected lead %s, got %s", testCase.lead, lead)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/notifications/ -run TestLeadIsProportionalAndBounded -v`
Expected: FAIL — `policy.Lead undefined`.

- [ ] **Step 3: Implement Lead**

Create `internal/notifications/lead.go`:

```go
package notifications

import "time"

// Lead returns how far ahead of the deadline a warning for a window of this
// length should be sent, and whether to send one at all. The lead is a fixed
// fraction of the window, capped so a very long retention window does not
// warn weeks in advance, and floored so a very short stop window does not
// produce a notice too late to act on. At Max*Divisor the two bounds meet, so
// the function is continuous: with the defaults, a three-day window yields
// exactly the 24-hour cap from either direction.
func (p LeadPolicy) Lead(window time.Duration) (time.Duration, bool) {
	if window <= 0 || p.Divisor < 1 {
		return 0, false
	}
	lead := window / time.Duration(p.Divisor)
	if lead > p.Max {
		lead = p.Max
	}
	if lead < p.Min {
		return 0, false
	}
	return lead, true
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/notifications/ -run TestLeadIsProportionalAndBounded -v`
Expected: PASS, all nine subtests.

- [ ] **Step 5: Use the policy in EnqueueTimeoutWarnings**

In `internal/notifications/service.go`, replace the fixed `s.warningLead` comparison. The window comes from the workspace record, chosen by phase:

```go
		var window time.Duration
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
```

Rename the `warningLead` field on `Service` to `leadPolicy LeadPolicy`.

- [ ] **Step 6: Write the failing config test**

In `internal/config/config_test.go`, change the defaults assertion at line 27 to expect `cfg.EmailWarningLeadDivisor == 3 && cfg.EmailWarningLeadMax == 24*time.Hour && cfg.EmailWarningLeadMin == time.Hour`, and drop `cfg.EmailWarningLeadTime`. In the override test, replace `"COWS_EMAIL_WARNING_LEAD_TIME": "2h"` with:

```go
		"COWS_EMAIL_WARNING_LEAD_DIVISOR": "4",
		"COWS_EMAIL_WARNING_LEAD_MAX":     "12h",
		"COWS_EMAIL_WARNING_LEAD_MIN":     "30m",
```

and the assertion to `cfg.EmailWarningLeadDivisor != 4 || cfg.EmailWarningLeadMax != 12*time.Hour || cfg.EmailWarningLeadMin != 30*time.Minute`.

Add a rejection test:

```go
func TestInvalidWarningLeadBoundsAreRejected(t *testing.T) {
	cases := map[string]map[string]string{
		"zero divisor":       {"COWS_EMAIL_WARNING_LEAD_DIVISOR": "0"},
		"minimum above max":  {"COWS_EMAIL_WARNING_LEAD_MIN": "48h"},
		"removed legacy key": {"COWS_EMAIL_WARNING_LEAD_TIME": "24h"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			lookup := func(key string) (string, bool) {
				value, ok := env[key]
				return value, ok
			}
			if _, err := load(nil, lookup); err == nil {
				t.Fatal("expected the configuration to be rejected")
			}
		})
	}
}
```

- [ ] **Step 7: Implement the config changes**

In `internal/config/config.go`:
- Replace the `EmailWarningLeadTime time.Duration` field with `EmailWarningLeadDivisor int`, `EmailWarningLeadMax time.Duration`, `EmailWarningLeadMin time.Duration`.
- Replace the `COWS_EMAIL_WARNING_LEAD_TIME` lookup with `COWS_EMAIL_WARNING_LEAD_DIVISOR` (default `"3"`), `COWS_EMAIL_WARNING_LEAD_MAX` (default `"24h"`), `COWS_EMAIL_WARNING_LEAD_MIN` (default `"1h"`).
- Replace the `email-warning-lead-time` flag with `email-warning-lead-divisor`, `email-warning-lead-max`, `email-warning-lead-min`.
- Parse and validate: divisor is an int ≥ 1, both durations positive, `min <= max`.
- Reject the removed key explicitly, so an operator who set it is told rather than silently getting new behavior:

```go
	// COWS_EMAIL_WARNING_LEAD_TIME was replaced by a window-relative policy in
	// decision 0027. Fail rather than silently changing warning behavior under
	// a key that still looks like it works.
	if _, ok := lookup("COWS_EMAIL_WARNING_LEAD_TIME"); ok {
		return Config{}, errors.New("COWS_EMAIL_WARNING_LEAD_TIME was removed; use COWS_EMAIL_WARNING_LEAD_DIVISOR, COWS_EMAIL_WARNING_LEAD_MAX, and COWS_EMAIL_WARNING_LEAD_MIN")
	}
```

- [ ] **Step 8: Wire it in main.go**

```go
		notificationService, err = notifications.New(store, sender, notifications.LeadPolicy{
			Divisor: cfg.EmailWarningLeadDivisor,
			Max:     cfg.EmailWarningLeadMax,
			Min:     cfg.EmailWarningLeadMin,
		}, cfg.EmailRetryInterval)
```

- [ ] **Step 9: Run the tests**

Run: `go test ./internal/config/... ./internal/notifications/... -v`
Expected: PASS.

- [ ] **Step 10: Full verification and commit**

```bash
gofmt -l $(git ls-files '*.go') && go vet ./... && go test ./...
git add -A
git commit -m "Derive warning lead times from the timeout window"
```

---

## Task 4: Workspace deletion notices

**Files:**
- Create: `internal/notifications/messages.go` addition — `WorkspaceDeletedMessage`
- Modify: `internal/workspace/workspace.go` (`DeleteWorkspace` around lines 847 and 919-930; `RunTimeouts` `TimeoutActionDelete` branch around lines 1283-1300)
- Test: `internal/workspace/lifecycle_test.go`

**Interfaces:**
- Consumes: `domain.EmailMessage`, `store.UpsertEmailMessage`, `store.CancelEmailMessagesForWorkspace`.
- Produces: `notifications.WorkspaceDeletedMessage(workspaceName, reason string, deletedAt time.Time, storageRetained bool) (subject, body string)` and the unexported `(*Service).enqueueDeletionNotice` on the workspace service.

`internal/notifications` imports `internal/workspace`, so the workspace service must **not** import `internal/notifications` — that would be an import cycle. The workspace service therefore builds the message body itself. Put `WorkspaceDeletedMessage` in `internal/notifications` for consistency with the other builders **only if** no cycle results; it does result, so instead put it in `internal/workspace/notice.go` and have `internal/notifications` not reference it.

**Corrected file list:** create `internal/workspace/notice.go` rather than adding to `internal/notifications/messages.go`.

- [ ] **Step 1: Write the failing test**

Add to `internal/workspace/lifecycle_test.go`:

```go
func TestTimeoutDeletionQueuesAnOwnerNotice(t *testing.T) {
	// Arrange a stopped workspace whose retention window has expired, run the
	// timeout worker, and assert the owner has a queued deletion notice that
	// survives the cancellation of the workspace's pending warnings.
	ctx := context.Background()
	service, store, base := timeoutTestService(t)
	// ... existing helper sets up template, owner, and fake runtime.

	if err := service.RunTimeouts(ctx); err != nil {
		t.Fatalf("run timeouts: %v", err)
	}
	pending, err := store.ListPendingEmailMessages(ctx, base.Add(time.Hour), 50)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	var found bool
	for _, message := range pending {
		if message.Kind == domain.EmailKindWorkspaceDeleted {
			found = true
			if strings.Contains(message.Body, "runtime-1") {
				t.Fatal("deletion notice must not contain a runtime identifier")
			}
		}
	}
	if !found {
		t.Fatal("expected a queued workspace deletion notice")
	}
}

func TestOwnerInitiatedDeletionQueuesNoNotice(t *testing.T) {
	ctx := context.Background()
	service, store, base := timeoutTestService(t)
	// Owner deletes their own stopped workspace.
	if err := service.DeleteWorkspace(ctx, "owner-1", "workspace-1"); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
	pending, err := store.ListPendingEmailMessages(ctx, base.Add(time.Hour), 50)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	for _, message := range pending {
		if message.Kind == domain.EmailKindWorkspaceDeleted {
			t.Fatal("an owner deleting their own workspace must not be mailed about it")
		}
	}
}
```

If `timeoutTestService` does not exist, build the fixtures the way the neighbouring tests in `internal/workspace/lifecycle_test.go` already do; do not invent a new pattern.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/workspace/ -run 'Notice' -v`
Expected: FAIL — no `workspace_deleted` message is queued.

- [ ] **Step 3: Write the message builder**

Create `internal/workspace/notice.go`:

```go
package workspace

import (
	"fmt"
	"time"
)

// deletionNoticeMessage describes a deletion the owner did not perform. It
// names the workspace and the reason and nothing else: no runtime identifier,
// host path, volume name, or archive location ever reaches a mailbox.
func deletionNoticeMessage(workspaceName, reason string, deletedAt time.Time, storageRetained bool) (string, string) {
	body := fmt.Sprintf(
		"Your COWS workspace %q was deleted at %s UTC (%s).",
		workspaceName, deletedAt.UTC().Format("2006-01-02 15:04"), reason)
	if storageRetained {
		body += "\n\nIts storage was retained and can be reattached to a new workspace from the storage page."
	}
	body += "\n\nThis is an advisory notification."
	return "COWS workspace deleted", body
}
```

- [ ] **Step 4: Add the enqueue helper**

In `internal/workspace/workspace.go`:

```go
// enqueueDeletionNotice queues an advisory notice for a deletion the owner did
// not perform. It is best-effort by design: the deletion has already happened
// and cannot be undone because mail could not be queued, so the error is
// returned only for the caller to log.
func (s *Service) enqueueDeletionNotice(ctx context.Context, value domain.Workspace, reason string, storageRetained bool) error {
	owner, err := s.store.FindUserByID(ctx, value.OwnerUserID)
	if err != nil || owner.Disabled || owner.Email == "" {
		return nil
	}
	now := s.now().UTC()
	subject, body := deletionNoticeMessage(value.Name, reason, now, storageRetained)
	return s.store.UpsertEmailMessage(ctx, domain.EmailMessage{
		Kind:          domain.EmailKindWorkspaceDeleted,
		UserID:        value.OwnerUserID,
		Recipient:     owner.Email,
		Subject:       subject,
		Body:          body,
		DedupeKey:     "workspace:" + value.ID + ":" + domain.EmailKindWorkspaceDeleted,
		Status:        "pending",
		NextAttemptAt: now,
		CreatedAt:     now,
	})
}
```

- [ ] **Step 5: Call it from the two deletion paths**

In the `TimeoutActionDelete` branch of `RunTimeouts`, **after** the existing `CancelEmailMessagesForWorkspace` call and after `finishOperation` succeeds — the cancel would otherwise cancel the notice it just queued:

```go
				s.recordAudit(ctx, domain.AuditEvent{EventType: "workspace.timeout_container_deleted", TargetType: "workspace", TargetID: value.ID})
				// Best-effort: a queue failure must not fail a deletion that
				// has already happened.
				_ = s.enqueueDeletionNotice(ctx, value, "the stopped-workspace retention period expired", false)
```

In `DeleteWorkspace`, in **both** the `RuntimeID == ""` early-return branch and the main path, immediately before each `return nil` / after each `recordAudit`, and only when the actor is not the owner:

```go
	if actorID != value.OwnerUserID {
		_ = s.enqueueDeletionNotice(ctx, value, "an administrator deleted it", len(retainedVolumes) > 0 || retainedDirectory != nil)
	}
```

In the early-return branch there are no `retainedVolumes`; use `retainedDirectory != nil` there.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/workspace/ -v`
Expected: PASS.

- [ ] **Step 7: Full verification and commit**

```bash
gofmt -l $(git ls-files '*.go') && go vet ./... && go test ./...
git add -A
git commit -m "Notify owners when a workspace is deleted for them"
```

---

## Task 5: Token purpose column

Lets invitations reuse the hashed single-use token machinery without letting either purpose be spent on the other.

**Files:**
- Create: `internal/database/migrations/0029_reset_token_purpose.sql`
- Modify: `internal/domain/password_reset.go`
- Modify: `internal/repository/repository.go:71-73`
- Modify: `internal/repository/sqlite/store.go:188-225, 1058-1075`
- Modify: `internal/auth/service.go:202-227`
- Test: `internal/auth/service_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `domain.PasswordResetToken.Purpose string`; the constants `domain.TokenPurposeReset = "reset"` and `domain.TokenPurposeInvitation = "invitation"`; `Store.ResetPasswordUsingToken(ctx, tokenHash, purpose, passwordHash string, now time.Time) (domain.User, error)`.

- [ ] **Step 1: Write the failing test**

Add to `internal/auth/service_test.go`:

```go
func TestATokenCannotBeSpentOnTheOtherPurpose(t *testing.T) {
	ctx := context.Background()
	service, store, _ := authTestService(t)
	user, err := service.CreateUser(ctx, "admin-1", CreateUserInput{
		Username: "invitee", Email: "invitee@example.test", DisplayName: "Invitee",
		Password: "initial-password-1", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	request, err := service.RequestPasswordReset(ctx, user.Username)
	if err != nil {
		t.Fatalf("request reset: %v", err)
	}
	// A reset token offered to the invitation path must be refused.
	if _, err := store.ResetPasswordUsingToken(ctx, hashToken(request.Token), domain.TokenPurposeInvitation, "hash", time.Now().UTC()); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected the invitation path to refuse a reset token, got %v", err)
	}
	// The same token still works for its own purpose.
	if _, err := store.ResetPasswordUsingToken(ctx, hashToken(request.Token), domain.TokenPurposeReset, "hash", time.Now().UTC()); err != nil {
		t.Fatalf("reset with the correct purpose: %v", err)
	}
}
```

If `authTestService` does not exist, follow the fixture pattern already used by the neighbouring tests in that file.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/auth/ -run TestATokenCannotBeSpentOnTheOtherPurpose -v`
Expected: FAIL — too many arguments to `ResetPasswordUsingToken`.

- [ ] **Step 3: Write the migration**

Create `internal/database/migrations/0029_reset_token_purpose.sql`:

```sql
ALTER TABLE password_reset_tokens
    ADD COLUMN purpose TEXT NOT NULL DEFAULT 'reset'
    CHECK (purpose IN ('reset', 'invitation'));

DROP INDEX password_reset_tokens_user_idx;
CREATE INDEX password_reset_tokens_user_idx
    ON password_reset_tokens(user_id, purpose, expires_at);
```

- [ ] **Step 4: Add the domain constants and field**

In `internal/domain/password_reset.go`:

```go
// Token purposes. A token issued for one purpose is never accepted by the
// other's consumption path.
const (
	TokenPurposeReset      = "reset"
	TokenPurposeInvitation = "invitation"
)

type PasswordResetToken struct {
	TokenHash string
	UserID    string
	Purpose   string
	ExpiresAt time.Time
	CreatedAt time.Time
}
```

- [ ] **Step 5: Thread purpose through the store**

`CreatePasswordResetToken` (`internal/repository/sqlite/store.go:1058`) currently deletes every token for the user before inserting. Scope that delete to the same purpose, so issuing an invitation does not silently void a reset in flight:

```go
	if _, err := tx.ExecContext(ctx, "DELETE FROM password_reset_tokens WHERE user_id = ? AND purpose = ?", token.UserID, token.Purpose); err != nil {
```

and add `purpose` to the INSERT column list and values.

`ResetPasswordUsingToken` gains a `purpose` parameter and adds `AND t.purpose = ?` to its SELECT. Everything else in that transaction is unchanged.

Update the `PasswordResetRepository` interface signature and the `auth` call site at `internal/auth/service.go:223` to pass `Purpose: domain.TokenPurposeReset`. Find the `ResetPasswordUsingToken` caller in `internal/auth/service.go` and pass `domain.TokenPurposeReset`.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/auth/ ./internal/repository/... -v`
Expected: PASS.

- [ ] **Step 7: Full verification and commit**

```bash
gofmt -l $(git ls-files '*.go') && go vet ./... && go test ./...
git add -A
git commit -m "Give reset tokens an explicit purpose"
```

---

## Task 6: Invitations on administrator user creation

**Files:**
- Create: `internal/auth/invitation.go`
- Create: `internal/auth/invitation_test.go`
- Modify: `internal/auth/service.go` (`prepareUser`, `CreateUser`, `validPassword` usage)
- Modify: `internal/notifications/service.go` — add `EnqueueInvitation`
- Modify: `internal/notifications/messages.go` — add `InvitationMessage`
- Modify: `internal/web/server.go:2301-2329`
- Modify: `web/templates/pages/admin-users-new.html`
- Modify: `internal/config/config.go`

**Interfaces:**
- Consumes: `domain.TokenPurposeInvitation` (Task 5), `notifications.Service` (Task 2).
- Produces:
  - `auth.ErrInvitationUnavailable`
  - `func (s *Service) CreateUserWithInvitation(ctx context.Context, actorID string, input CreateUserInput) (domain.User, InvitationRequest, error)`
  - `type InvitationRequest struct { User domain.User; Token string; ExpiresAt time.Time }`
  - `func (s *Notifications) EnqueueInvitation(ctx context.Context, userID, recipient, rawToken, baseURL string, expiresAt time.Time) error` on `notifications.Service`
  - `config.Config.InvitationLifetime time.Duration`

- [ ] **Step 1: Write the failing tests**

Create `internal/auth/invitation_test.go`:

```go
package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/cows-project/cows/internal/domain"
)

func TestBlankPasswordCreatesAnUnusableAccountAndAnInvitation(t *testing.T) {
	ctx := context.Background()
	service, store, _ := authTestService(t)
	service.invitationsAvailable = true

	user, invitation, err := service.CreateUserWithInvitation(ctx, "admin-1", CreateUserInput{
		Username: "invitee", Email: "invitee@example.test", DisplayName: "Invitee", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatalf("create user with invitation: %v", err)
	}
	if invitation.Token == "" {
		t.Fatal("expected an invitation token")
	}
	if !user.MustChangePassword {
		t.Fatal("an invited user must be forced to choose a password")
	}
	// No password may authenticate against the placeholder hash.
	for _, attempt := range []string{"", "password", "invitee", invitation.Token} {
		if _, err := service.Authenticate(ctx, "invitee", attempt); err == nil {
			t.Fatalf("password %q authenticated against an invited account", attempt)
		}
	}
	_ = store
}

func TestBlankPasswordIsRejectedWithoutADeliverableAddress(t *testing.T) {
	ctx := context.Background()
	service, _, _ := authTestService(t)
	service.invitationsAvailable = true

	if _, _, err := service.CreateUserWithInvitation(ctx, "admin-1", CreateUserInput{
		Username: "invitee", DisplayName: "Invitee", Role: domain.RoleUser,
	}); !errors.Is(err, ErrInvitationUnavailable) {
		t.Fatalf("expected ErrInvitationUnavailable for a missing address, got %v", err)
	}

	service.invitationsAvailable = false
	if _, _, err := service.CreateUserWithInvitation(ctx, "admin-1", CreateUserInput{
		Username: "invitee2", Email: "invitee2@example.test", DisplayName: "Invitee", Role: domain.RoleUser,
	}); !errors.Is(err, ErrInvitationUnavailable) {
		t.Fatalf("expected ErrInvitationUnavailable when email is disabled, got %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/auth/ -run Invitation -v`
Expected: FAIL — `CreateUserWithInvitation undefined`.

- [ ] **Step 3: Implement the invitation path**

Create `internal/auth/invitation.go`:

```go
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/cows-project/cows/internal/domain"
)

// ErrInvitationUnavailable means an account was requested without a password
// but cannot be invited: no address to send to, or email delivery disabled.
// Creating an account nobody can reach and nobody can log into must fail.
var ErrInvitationUnavailable = errors.New("invitation cannot be delivered")

type InvitationRequest struct {
	User      domain.User
	Token     string
	ExpiresAt time.Time
}

// unusablePasswordHash returns a bcrypt hash of a random value that is
// immediately discarded, so no password can ever match it. This is
// deliberately not an empty string or a fixed sentinel: both would be a
// guessable shared secret across every invited account.
func unusablePasswordHash() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate placeholder secret: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(base64.RawURLEncoding.EncodeToString(secret)), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash placeholder secret: %w", err)
	}
	return string(hash), nil
}

// CreateUserWithInvitation creates an account with a password when one is
// given, and otherwise an account that can only be opened through a mailed
// single-use invitation. The returned token is raw and must be placed only in
// a link; only its hash is stored.
func (s *Service) CreateUserWithInvitation(ctx context.Context, actorID string, input CreateUserInput) (domain.User, InvitationRequest, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.User{}, InvitationRequest{}, err
	}
	if strings.TrimSpace(input.Password) != "" {
		user, err := s.createUser(ctx, input)
		if err != nil {
			return domain.User{}, InvitationRequest{}, err
		}
		s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: auditUserCreated, TargetType: "user", TargetID: user.ID, Metadata: map[string]string{"role": string(user.Role)}})
		return user, InvitationRequest{}, nil
	}
	if !s.invitationsAvailable || strings.TrimSpace(input.Email) == "" {
		return domain.User{}, InvitationRequest{}, ErrInvitationUnavailable
	}
	// prepareUser validates the password, so hand it a value that passes and
	// replace the resulting hash with an unusable one.
	placeholder, err := GenerateTemporaryPassword(20)
	if err != nil {
		return domain.User{}, InvitationRequest{}, err
	}
	input.Password = placeholder
	user, _, err := s.prepareUser(ctx, input, true)
	if err != nil {
		return domain.User{}, InvitationRequest{}, err
	}
	hash, err := unusablePasswordHash()
	if err != nil {
		return domain.User{}, InvitationRequest{}, err
	}
	if err := s.store.CreateUser(ctx, user, hash); err != nil {
		return domain.User{}, InvitationRequest{}, err
	}
	rawToken, err := randomToken()
	if err != nil {
		return domain.User{}, InvitationRequest{}, fmt.Errorf("create invitation token: %w", err)
	}
	now := s.now().UTC()
	expiresAt := now.Add(s.invitationLifetime)
	if err := s.store.CreatePasswordResetToken(ctx, domain.PasswordResetToken{
		TokenHash: hashToken(rawToken), UserID: user.ID, Purpose: domain.TokenPurposeInvitation,
		ExpiresAt: expiresAt, CreatedAt: now,
	}); err != nil {
		return domain.User{}, InvitationRequest{}, err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: auditUserCreated, TargetType: "user", TargetID: user.ID, Metadata: map[string]string{"role": string(user.Role), "credential": "invitation"}})
	return user, InvitationRequest{User: user, Token: rawToken, ExpiresAt: expiresAt}, nil
}
```

Add to the `Service` struct in `internal/auth/service.go`:

```go
	invitationsAvailable bool
	invitationLifetime   time.Duration
```

and a setter, following the `SetNetworkIsolation` precedent in `internal/workspace`:

```go
// SetInvitationPolicy enables password-less account creation. It is off until
// the caller confirms email delivery and an external base URL are configured.
func (s *Service) SetInvitationPolicy(available bool, lifetime time.Duration) {
	s.invitationsAvailable = available
	s.invitationLifetime = lifetime
}
```

Default `invitationLifetime` to 24h in `New` so a caller that forgets the setter still gets a sane bound.

- [ ] **Step 4: Add the notification enqueue and message**

In `internal/notifications/messages.go`:

```go
// InvitationMessage carries a single-use link that sets the first password.
func InvitationMessage(displayName, acceptURL string, expiresAt time.Time) (string, string) {
	return "Your COWS account", fmt.Sprintf(
		"An account was created for you on COWS%s.\n\nOpen this link before %s UTC to choose your password and sign in:\n\n%s\n\nThe link can be used once. If it expires, ask an administrator to send a new one.",
		greetingSuffix(displayName), expiresAt.UTC().Format("2006-01-02 15:04"), acceptURL)
}

func greetingSuffix(displayName string) string {
	if strings.TrimSpace(displayName) == "" {
		return ""
	}
	return ", " + strings.TrimSpace(displayName)
}
```

In `internal/notifications/service.go`, generalize the URL construction already in `EnqueuePasswordReset` into a shared helper and add `EnqueueInvitation`:

```go
// tokenURL builds a link into the configured external base URL. It refuses a
// base URL carrying a query or fragment so the token cannot be smuggled into
// an existing parameter.
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

func (s *Service) EnqueueInvitation(ctx context.Context, userID, recipient, displayName, rawToken, baseURL string, expiresAt time.Time) error {
	if s.sender == nil || userID == "" || recipient == "" || rawToken == "" {
		return nil
	}
	acceptURL, err := tokenURL(baseURL, "/invitation/accept", rawToken)
	if err != nil {
		return err
	}
	subject, body := InvitationMessage(displayName, acceptURL, expiresAt)
	now := s.now().UTC()
	return s.store.UpsertEmailMessage(ctx, domain.EmailMessage{
		Kind: domain.EmailKindInvitation, UserID: userID, Recipient: recipient,
		Subject: subject, Body: body, Status: "pending", NextAttemptAt: now, CreatedAt: now,
	})
}
```

Rewrite `EnqueuePasswordReset` to use `tokenURL(baseURL, "/password/reset/confirm", rawToken)` and `PasswordResetMessage`.

- [ ] **Step 5: Update the web handler**

In `internal/web/server.go:2301-2329`, call `CreateUserWithInvitation` and enqueue when a token comes back:

```go
	newUser, invitation, err := s.auth.CreateUserWithInvitation(r.Context(), user.ID, auth.CreateUserInput{
		Username:    form.Username,
		Email:       form.Email,
		DisplayName: form.DisplayName,
		Password:    r.FormValue("password"),
		Role:        domain.Role(form.Role),
	})
	if err != nil {
		form.Error = userFormError(err)
		s.render(w, http.StatusBadRequest, "admin-users-new-page", pageData{Title: "Create user | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r), Form: form, InvitationsEnabled: s.options.InvitationsEnabled})
		return
	}
	if invitation.Token != "" && s.options.Notifications != nil {
		// Best-effort: the account exists either way, and an administrator can
		// resend. Do not surface a delivery error as a creation failure.
		_ = s.options.Notifications.EnqueueInvitation(r.Context(), newUser.ID, newUser.Email, newUser.DisplayName, invitation.Token, s.options.ExternalBaseURL, invitation.ExpiresAt)
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
```

Add `InvitationsEnabled bool` to `web.Options` and to `pageData`, set from `PasswordResetEnabled`'s condition in `cmd/cows/main.go` (`notificationService != nil && cfg.ExternalBaseURL != ""`), and call `authService.SetInvitationPolicy(notificationService != nil && cfg.ExternalBaseURL != "", cfg.InvitationLifetime)` there.

Extend `userFormError` so `auth.ErrInvitationUnavailable` renders "Set a temporary password, or add an email address so an invitation can be sent." — never a raw error.

- [ ] **Step 6: Update the create form**

In `web/templates/pages/admin-users-new.html`, replace the password block:

```html
    <label for="password">Temporary password {{ if .InvitationsEnabled }}<span class="muted">optional — leave blank to email an invitation</span>{{ end }}</label>
    <input id="password" name="password" type="password" minlength="12" maxlength="72" autocomplete="new-password"{{ if not .InvitationsEnabled }} required{{ end }}>
```

and change the email label from `optional for now` to `<span class="muted">required for an invitation</span>`.

- [ ] **Step 7: Add the config key**

Add `InvitationLifetime time.Duration` to `config.Config`, read from `COWS_INVITATION_LIFETIME` with default `"24h"`, with an `invitation-lifetime` flag, rejected when not positive. Extend the config defaults test to assert `cfg.InvitationLifetime == 24*time.Hour`.

- [ ] **Step 8: Run the tests**

Run: `go test ./internal/auth/ ./internal/notifications/ ./internal/config/ ./internal/web/ -v`
Expected: PASS.

- [ ] **Step 9: Full verification and commit**

```bash
gofmt -l $(git ls-files '*.go') && go vet ./... && go test ./...
git add -A
git commit -m "Invite administrator-created accounts by email"
```

---

## Task 7: Accepting an invitation

**Files:**
- Modify: `internal/auth/invitation.go` — `AcceptInvitation`
- Modify: `internal/web/server.go` — two routes and handlers near the password-reset handlers (lines 586-589, 756-800)
- Create: `web/templates/pages/invitation-accept.html`
- Test: `internal/web/server_test.go`, `internal/auth/invitation_test.go`

**Interfaces:**
- Consumes: `Store.ResetPasswordUsingToken(ctx, tokenHash, purpose, passwordHash, now)` (Task 5), `auth.InvitationRequest` (Task 6).
- Produces: `func (s *Service) AcceptInvitation(ctx context.Context, rawToken, password, confirmation string) (domain.User, error)`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/auth/invitation_test.go`:

```go
func TestAcceptingAnInvitationSetsThePasswordOnce(t *testing.T) {
	ctx := context.Background()
	service, _, _ := authTestService(t)
	service.SetInvitationPolicy(true, 24*time.Hour)

	_, invitation, err := service.CreateUserWithInvitation(ctx, "admin-1", CreateUserInput{
		Username: "invitee", Email: "invitee@example.test", DisplayName: "Invitee", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatalf("create user with invitation: %v", err)
	}
	accepted, err := service.AcceptInvitation(ctx, invitation.Token, "chosen-password-1", "chosen-password-1")
	if err != nil {
		t.Fatalf("accept invitation: %v", err)
	}
	if accepted.MustChangePassword {
		t.Fatal("accepting an invitation must clear the forced password change")
	}
	if _, err := service.Authenticate(ctx, "invitee", "chosen-password-1"); err != nil {
		t.Fatalf("authenticate after accepting: %v", err)
	}
	// Single use.
	if _, err := service.AcceptInvitation(ctx, invitation.Token, "another-password-1", "another-password-1"); err == nil {
		t.Fatal("an invitation token must be single-use")
	}
}

func TestExpiredInvitationIsRefused(t *testing.T) {
	ctx := context.Background()
	service, _, base := authTestService(t)
	service.SetInvitationPolicy(true, time.Hour)
	service.now = func() time.Time { return base }

	_, invitation, err := service.CreateUserWithInvitation(ctx, "admin-1", CreateUserInput{
		Username: "invitee", Email: "invitee@example.test", DisplayName: "Invitee", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatalf("create user with invitation: %v", err)
	}
	service.now = func() time.Time { return base.Add(2 * time.Hour) }
	if _, err := service.AcceptInvitation(ctx, invitation.Token, "chosen-password-1", "chosen-password-1"); err == nil {
		t.Fatal("an expired invitation must be refused")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/auth/ -run Invitation -v`
Expected: FAIL — `AcceptInvitation undefined`.

- [ ] **Step 3: Implement AcceptInvitation**

Append to `internal/auth/invitation.go`:

```go
// AcceptInvitation consumes a single-use invitation token, sets the first
// password, clears the forced password change, and invalidates every session
// for the account. It mirrors the reset path deliberately: the only
// differences are the token purpose and the audit event.
func (s *Service) AcceptInvitation(ctx context.Context, rawToken, password, confirmation string) (domain.User, error) {
	if password != confirmation {
		return domain.User{}, ErrInvalidInput
	}
	if !validPassword(password) {
		return domain.User{}, ErrInvalidInput
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return domain.User{}, fmt.Errorf("hash password: %w", err)
	}
	user, err := s.store.ResetPasswordUsingToken(ctx, hashToken(rawToken), domain.TokenPurposeInvitation, string(hash), s.now().UTC())
	if err != nil {
		return domain.User{}, err
	}
	s.recordAudit(ctx, domain.AuditEvent{EventType: "user.invitation_accepted", TargetType: "user", TargetID: user.ID})
	return user, nil
}
```

- [ ] **Step 4: Add the web routes**

In `internal/web/server.go` next to the password-reset routes (line 589):

```go
	mux.HandleFunc("GET /invitation/accept", s.invitationAcceptGet)
	mux.HandleFunc("POST /invitation/accept", s.invitationAcceptPost)
```

Handlers modelled directly on `passwordResetConfirmGet`/`passwordResetConfirmPost` (lines 756-800): 404 when `!s.options.InvitationsEnabled`; the GET redirects to `/login` on a missing or over-long token; the POST enforces `MaxBytesReader`, `validCSRF`, and renders the same generic failure for every invalid, expired, or already-used token; on success it redirects to `/login` with a notice. Reuse `passwordResetFormData` for the token field rather than adding a parallel struct.

- [ ] **Step 5: Add the template**

Create `web/templates/pages/invitation-accept.html`, copying `password-reset-confirm.html` and changing the heading to "Set your password", the intro to explain that this completes account setup, and the form action to `/invitation/accept`. Register it wherever `password-reset-confirm-page` is registered.

- [ ] **Step 6: Write the web test**

Add to `internal/web/server_test.go` a test asserting that `POST /invitation/accept` without a CSRF token returns 403, and that both routes return 404 when `InvitationsEnabled` is false. Follow the existing password-reset handler tests in that file.

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/auth/ ./internal/web/ -v`
Expected: PASS.

- [ ] **Step 8: Full verification and commit**

```bash
gofmt -l $(git ls-files '*.go') && go vet ./... && go test ./...
git add -A
git commit -m "Let invited users set their first password"
```

---

## Task 8: Invitations for imported users and resend

**Files:**
- Modify: `internal/auth/import.go:82-140`
- Modify: `internal/web/server.go` — import commit handler, resend route
- Modify: `web/templates/pages/admin-user-edit.html`
- Test: `internal/auth/import_test.go`

**Interfaces:**
- Consumes: `unusablePasswordHash`, `InvitationRequest`, `Service.invitationsAvailable`, `Service.invitationLifetime` (Task 6).
- Produces: `ImportedUserResult.Invitation InvitationRequest`; `func (s *Service) ResendInvitation(ctx context.Context, actorID, targetUserID string) (InvitationRequest, error)`.

- [ ] **Step 1: Write the failing test**

Add to `internal/auth/import_test.go`:

```go
func TestImportIssuesInvitationsWhenAvailable(t *testing.T) {
	ctx := context.Background()
	service, _, _ := authTestService(t)
	service.SetInvitationPolicy(true, 24*time.Hour)

	results, err := service.ImportUsers(ctx, "admin-1", []ImportUserInput{
		{Username: "rowone", Email: "rowone@example.test", DisplayName: "Row One"},
		{Username: "rowtwo", DisplayName: "Row Two"},
	}, nil)
	if err != nil {
		t.Fatalf("import users: %v", err)
	}
	if results[0].Invitation.Token == "" {
		t.Fatal("a row with an address must get an invitation")
	}
	if results[0].Password != "" {
		t.Fatal("an invited row must not also carry a temporary password")
	}
	// A row without an address cannot be invited and keeps the temporary password.
	if results[1].Invitation.Token != "" || results[1].Password == "" {
		t.Fatal("a row without an address must fall back to a temporary password")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/auth/ -run TestImportIssuesInvitations -v`
Expected: FAIL — `Invitation` is not a field of `ImportedUserResult`.

- [ ] **Step 3: Implement import invitations**

Add `Invitation InvitationRequest` to `ImportedUserResult`. In the new-user branch of `ImportUsers` (`internal/auth/import.go:114-128`), branch on availability:

```go
		invitable := s.invitationsAvailable && strings.TrimSpace(input.Email) != ""
		password, passwordErr := GenerateTemporaryPassword(20)
		if passwordErr != nil {
			return nil, passwordErr
		}
		user, _, prepareErr := s.prepareUser(ctx, CreateUserInput{Username: input.Username, Email: input.Email, DisplayName: input.DisplayName, Password: password, Role: domain.RoleUser}, true)
		if prepareErr != nil {
			return nil, prepareErr
		}
		hash := ""
		result := ImportedUserResult{ImportUserInput: input, UserID: user.ID}
		if invitable {
			// An invited row never gets a usable password, so nothing has to
			// be relayed out of band.
			placeholder, hashErr := unusablePasswordHash()
			if hashErr != nil {
				return nil, hashErr
			}
			hash = placeholder
			rawToken, tokenErr := randomToken()
			if tokenErr != nil {
				return nil, fmt.Errorf("create invitation token: %w", tokenErr)
			}
			now := s.now().UTC()
			invitations = append(invitations, domain.PasswordResetToken{
				TokenHash: hashToken(rawToken), UserID: user.ID, Purpose: domain.TokenPurposeInvitation,
				ExpiresAt: now.Add(s.invitationLifetime), CreatedAt: now,
			})
			result.Invitation = InvitationRequest{User: user, Token: rawToken, ExpiresAt: now.Add(s.invitationLifetime)}
		} else {
			generated, hashErr := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if hashErr != nil {
				return nil, hashErr
			}
			hash = string(generated)
			result.Password = password
		}
		entries = append(entries, repository.UserImportEntry{User: user, PasswordHash: hash, GroupIDs: append([]string(nil), groupIDs...)})
		results = append(results, result)
```

Declare `invitations := make([]domain.PasswordResetToken, 0, len(inputs))` above the loop and, after the existing `s.store.ImportUsers(ctx, entries)` succeeds, persist them:

```go
	for _, token := range invitations {
		if err := s.store.CreatePasswordResetToken(ctx, token); err != nil {
			return nil, err
		}
	}
```

They are created after the import so a failed import leaves no orphan tokens.

- [ ] **Step 4: Enqueue import invitations in the web handler**

In `adminUsersImportCommit`, after the service returns, loop the results and call `s.options.Notifications.EnqueueInvitation` for every result with a non-empty `Invitation.Token`, ignoring the error. The outbox already caps delivery at fifty messages per pass, so a large import drains over successive ticks rather than in one burst.

The results page must keep showing temporary passwords only for rows that actually have one.

- [ ] **Step 5: Implement resend**

Add to `internal/auth/invitation.go`:

```go
// ResendInvitation issues a fresh invitation for an account that has not yet
// been opened. CreatePasswordResetToken deletes the account's previous tokens
// of the same purpose, so the superseded link stops working.
func (s *Service) ResendInvitation(ctx context.Context, actorID, targetUserID string) (InvitationRequest, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return InvitationRequest{}, err
	}
	if !s.invitationsAvailable {
		return InvitationRequest{}, ErrInvitationUnavailable
	}
	target, err := s.store.FindUserByID(ctx, targetUserID)
	if err != nil {
		return InvitationRequest{}, err
	}
	if target.Disabled || strings.TrimSpace(target.Email) == "" {
		return InvitationRequest{}, ErrInvitationUnavailable
	}
	rawToken, err := randomToken()
	if err != nil {
		return InvitationRequest{}, fmt.Errorf("create invitation token: %w", err)
	}
	now := s.now().UTC()
	expiresAt := now.Add(s.invitationLifetime)
	if err := s.store.CreatePasswordResetToken(ctx, domain.PasswordResetToken{
		TokenHash: hashToken(rawToken), UserID: target.ID, Purpose: domain.TokenPurposeInvitation,
		ExpiresAt: expiresAt, CreatedAt: now,
	}); err != nil {
		return InvitationRequest{}, err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "user.invitation_resent", TargetType: "user", TargetID: target.ID})
	return InvitationRequest{User: target, Token: rawToken, ExpiresAt: expiresAt}, nil
}
```

Add `POST /admin/users/{id}/invitation` to the route table next to the other `/admin/users/{id}/...` routes (around line 634), with an administrator check, CSRF validation, a call to `ResendInvitation`, an enqueue, and a redirect back to the user edit page. Add the button to `web/templates/pages/admin-user-edit.html`, rendered only when `.InvitationsEnabled` and the target has an email address.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/auth/ ./internal/web/ -v`
Expected: PASS.

- [ ] **Step 7: Full verification and commit**

```bash
gofmt -l $(git ls-files '*.go') && go vet ./... && go test ./...
git add -A
git commit -m "Invite imported users and let administrators resend"
```

---

## Task 9: Welcome mail on self-registration

**Files:**
- Modify: `internal/notifications/messages.go` — `WelcomeMessage`
- Modify: `internal/notifications/service.go` — `EnqueueWelcome`
- Modify: `internal/web/server.go` — `registerPost`
- Test: `internal/notifications/service_test.go`

**Interfaces:**
- Consumes: `domain.EmailKindWelcome` (Task 1), `Service.store` (Task 2).
- Produces: `func (s *Service) EnqueueWelcome(ctx context.Context, userID, recipient, displayName, baseURL string) error`.

- [ ] **Step 1: Write the failing test**

```go
func TestWelcomeMessageCarriesNoToken(t *testing.T) {
	ctx := context.Background()
	store, base := notificationTestStore(t, domain.Workspace{
		ID: "workspace-1", OwnerUserID: "owner-1", TemplateID: "template-1", Name: "Research workspace",
		DesiredState: domain.DesiredWorkspaceStopped, CreatedAt: base, UpdatedAt: base,
	})
	sender := &fakeSender{}
	service, err := New(store, sender, LeadPolicy{Divisor: 3, Max: 24 * time.Hour, Min: time.Hour}, time.Minute)
	if err != nil {
		t.Fatalf("create notification service: %v", err)
	}
	service.now = func() time.Time { return base }
	if err := service.EnqueueWelcome(ctx, "owner-1", "owner@example.test", "Owner", "https://cows.example.test"); err != nil {
		t.Fatalf("enqueue welcome: %v", err)
	}
	if err := service.Deliver(ctx); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected one welcome message, got %d", len(sender.sent))
	}
	if strings.Contains(sender.sent[0].body, "token=") {
		t.Fatal("a welcome message must not carry a token; registration already chose a password")
	}
}
```

Note the fixture ordering: `base` is returned by `notificationTestStore`, so hoist the workspace literal's timestamps to a local `base := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)` declared before the call, matching the existing tests in the file.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/notifications/ -run TestWelcomeMessage -v`
Expected: FAIL — `EnqueueWelcome undefined`.

- [ ] **Step 3: Implement it**

In `internal/notifications/messages.go`:

```go
// WelcomeMessage confirms a self-registered account. It is informational and
// carries no token: the user chose their own password during registration,
// and COWS deliberately does not verify addresses (decision 0015).
func WelcomeMessage(displayName, baseURL string) (string, string) {
	return "Welcome to COWS", fmt.Sprintf(
		"Your COWS account is ready%s. Sign in at:\n\n%s\n\nIf you did not create this account, contact your administrator.",
		greetingSuffix(displayName), strings.TrimRight(baseURL, "/")+"/login")
}
```

In `internal/notifications/service.go`:

```go
func (s *Service) EnqueueWelcome(ctx context.Context, userID, recipient, displayName, baseURL string) error {
	if s.sender == nil || userID == "" || recipient == "" {
		return nil
	}
	subject, body := WelcomeMessage(displayName, baseURL)
	now := s.now().UTC()
	return s.store.UpsertEmailMessage(ctx, domain.EmailMessage{
		Kind: domain.EmailKindWelcome, UserID: userID, Recipient: recipient,
		Subject: subject, Body: body, Status: "pending", NextAttemptAt: now, CreatedAt: now,
	})
}
```

In `registerPost` in `internal/web/server.go`, after a successful `s.auth.Register`, best-effort enqueue when notifications are configured:

```go
	if s.options.Notifications != nil && registered.Email != "" {
		_ = s.options.Notifications.EnqueueWelcome(r.Context(), registered.ID, registered.Email, registered.DisplayName, s.options.ExternalBaseURL)
	}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/notifications/ ./internal/web/ -v`
Expected: PASS.

- [ ] **Step 5: Full verification and commit**

```bash
gofmt -l $(git ls-files '*.go') && go vet ./... && go test ./...
git add -A
git commit -m "Welcome self-registered accounts by email"
```

---

## Task 10: Password reset lifetime and throttling

**Files:**
- Modify: `internal/auth/service.go:82, 199-227`
- Modify: `internal/config/config.go`
- Modify: `internal/web/server.go:722-754`
- Test: `internal/auth/service_test.go`

**Interfaces:**
- Consumes: `store.CountRecentEmailMessages` (Task 1).
- Produces: `config.Config.PasswordResetLifetime time.Duration`, `config.Config.PasswordResetMinInterval time.Duration`; `func (s *Service) SetPasswordResetPolicy(lifetime, minInterval time.Duration)`.

- [ ] **Step 1: Write the failing test**

```go
func TestResetRequestsAreThrottledWithoutRevealingIt(t *testing.T) {
	ctx := context.Background()
	service, store, base := authTestService(t)
	service.SetPasswordResetPolicy(2*time.Hour, 5*time.Minute)
	service.now = func() time.Time { return base }
	user, err := service.CreateUser(ctx, "admin-1", CreateUserInput{
		Username: "member", Email: "member@example.test", DisplayName: "Member",
		Password: "initial-password-1", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := store.UpsertEmailMessage(ctx, domain.EmailMessage{
		Kind: domain.EmailKindPasswordReset, UserID: user.ID, Recipient: user.Email,
		Subject: "s", Body: "b", Status: "pending", NextAttemptAt: base, CreatedAt: base,
	}); err != nil {
		t.Fatalf("queue prior reset: %v", err)
	}
	service.now = func() time.Time { return base.Add(time.Minute) }
	request, err := service.RequestPasswordReset(ctx, "member")
	if err != nil {
		t.Fatalf("throttled request must not error: %v", err)
	}
	if request.Token != "" {
		t.Fatal("a throttled request must not issue a token")
	}
	// Past the interval the request succeeds again.
	service.now = func() time.Time { return base.Add(10 * time.Minute) }
	request, err = service.RequestPasswordReset(ctx, "member")
	if err != nil {
		t.Fatalf("request after the interval: %v", err)
	}
	if request.Token == "" {
		t.Fatal("expected a token once the interval has passed")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/auth/ -run TestResetRequestsAreThrottled -v`
Expected: FAIL — `SetPasswordResetPolicy undefined`.

- [ ] **Step 3: Implement the policy**

Replace the `passwordResetLifetime` constant at `internal/auth/service.go:82` with `passwordResetLifetime` and `passwordResetMinInterval` fields on `Service`, defaulted in `New` to 2h and 5m, plus:

```go
// SetPasswordResetPolicy sets how long a reset link lives and how rarely a
// single account can have one queued.
func (s *Service) SetPasswordResetPolicy(lifetime, minInterval time.Duration) {
	if lifetime > 0 {
		s.passwordResetLifetime = lifetime
	}
	if minInterval > 0 {
		s.passwordResetMinInterval = minInterval
	}
}
```

In `RequestPasswordReset`, after the account is resolved and before the token is created:

```go
	// Throttle per account. The caller's response is identical either way, so
	// this cannot become an account-existence oracle.
	recent, err := s.store.CountRecentEmailMessages(ctx, record.User.ID, domain.EmailKindPasswordReset, now.Add(-s.passwordResetMinInterval))
	if err != nil {
		return PasswordResetRequest{}, err
	}
	if recent > 0 {
		return PasswordResetRequest{}, nil
	}
```

and use `now.Add(s.passwordResetLifetime)` for the expiry.

- [ ] **Step 4: Add the config keys**

`COWS_PASSWORD_RESET_LIFETIME` (default `"2h"`, flag `password-reset-lifetime`) and `COWS_PASSWORD_RESET_MIN_INTERVAL` (default `"5m"`, flag `password-reset-min-interval`), both rejected when not positive. Call `authService.SetPasswordResetPolicy(cfg.PasswordResetLifetime, cfg.PasswordResetMinInterval)` in `cmd/cows/main.go`. Extend the config defaults test.

- [ ] **Step 5: Verify the login-page link is already correct**

`web/templates/pages/login.html:17` already renders the reset link behind `.PasswordResetEnabled`, and `cmd/cows/main.go:116` already derives that from `notificationService != nil && cfg.ExternalBaseURL != ""`. Add a regression test in `internal/web/server_test.go` asserting the link is absent when `PasswordResetEnabled` is false and present when it is true, so this cannot silently regress. No template change is needed.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/auth/ ./internal/config/ ./internal/web/ -v`
Expected: PASS.

- [ ] **Step 7: Full verification and commit**

```bash
gofmt -l $(git ls-files '*.go') && go vet ./... && go test ./...
git add -A
git commit -m "Make reset link lifetime configurable and throttle requests"
```

---

## Task 11: Administrator-triggered reset email

**Files:**
- Modify: `internal/auth/service.go` — `SendPasswordResetFor`
- Modify: `internal/web/server.go` — route and handler
- Modify: `web/templates/pages/admin-user-edit.html`
- Test: `internal/auth/service_test.go`

**Interfaces:**
- Consumes: `domain.TokenPurposeReset` (Task 5), `Service.passwordResetLifetime` (Task 10).
- Produces: `func (s *Service) SendPasswordResetFor(ctx context.Context, actorID, targetUserID string) (PasswordResetRequest, error)`.

- [ ] **Step 1: Write the failing test**

```go
func TestAdministratorTriggeredResetIssuesATokenForAnyRole(t *testing.T) {
	ctx := context.Background()
	service, _, _ := authTestService(t)
	user, err := service.CreateUser(ctx, "admin-1", CreateUserInput{
		Username: "member", Email: "member@example.test", DisplayName: "Member",
		Password: "initial-password-1", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	request, err := service.SendPasswordResetFor(ctx, "admin-1", user.ID)
	if err != nil {
		t.Fatalf("administrator reset: %v", err)
	}
	if request.Token == "" {
		t.Fatal("expected a reset token")
	}
	// A non-administrator must not be able to trigger one.
	if _, err := service.SendPasswordResetFor(ctx, user.ID, user.ID); err == nil {
		t.Fatal("a non-administrator must not trigger a reset for anyone")
	}
}

func TestAdministratorTriggeredResetRefusesDisabledOrAddresslessAccounts(t *testing.T) {
	ctx := context.Background()
	service, _, _ := authTestService(t)
	user, err := service.CreateUser(ctx, "admin-1", CreateUserInput{
		Username: "member", DisplayName: "Member",
		Password: "initial-password-1", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := service.SendPasswordResetFor(ctx, "admin-1", user.ID); err == nil {
		t.Fatal("an account without an address cannot be mailed a reset")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/auth/ -run TestAdministratorTriggeredReset -v`
Expected: FAIL — `SendPasswordResetFor undefined`.

- [ ] **Step 3: Implement it**

```go
// SendPasswordResetFor issues a reset token on an administrator's behalf, so
// no secret has to be relayed out of band. Unlike RequestPasswordReset it
// names the exact reason it refused: the caller is an authenticated
// administrator acting on a user they can already see, so there is nothing to
// protect from enumeration here. RecoverAdministrator remains for deployments
// without email.
func (s *Service) SendPasswordResetFor(ctx context.Context, actorID, targetUserID string) (PasswordResetRequest, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return PasswordResetRequest{}, err
	}
	target, err := s.store.FindUserByID(ctx, targetUserID)
	if err != nil {
		return PasswordResetRequest{}, err
	}
	if target.Disabled || strings.TrimSpace(target.Email) == "" {
		return PasswordResetRequest{}, ErrRecoveryTargetInvalid
	}
	rawToken, err := randomToken()
	if err != nil {
		return PasswordResetRequest{}, fmt.Errorf("create password reset token: %w", err)
	}
	now := s.now().UTC()
	expiresAt := now.Add(s.passwordResetLifetime)
	if err := s.store.CreatePasswordResetToken(ctx, domain.PasswordResetToken{
		TokenHash: hashToken(rawToken), UserID: target.ID, Purpose: domain.TokenPurposeReset,
		ExpiresAt: expiresAt, CreatedAt: now,
	}); err != nil {
		return PasswordResetRequest{}, err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "user.password_reset_sent", TargetType: "user", TargetID: target.ID})
	return PasswordResetRequest{User: target, Token: rawToken, ExpiresAt: expiresAt}, nil
}
```

This path is deliberately not throttled: it requires an authenticated administrator, so the abuse the throttle exists to prevent does not apply.

- [ ] **Step 4: Add the route, handler, and button**

`POST /admin/users/{id}/password-reset` beside the other `/admin/users/{id}/...` routes. The handler checks administrator, validates CSRF, calls `SendPasswordResetFor`, enqueues through `s.options.Notifications.EnqueuePasswordReset` on success, and redirects back to the user edit page. Add the button to `web/templates/pages/admin-user-edit.html`, rendered only when `.PasswordResetEnabled` and the target has an email address.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/auth/ ./internal/web/ -v`
Expected: PASS.

- [ ] **Step 6: Full verification and commit**

```bash
gofmt -l $(git ls-files '*.go') && go vet ./... && go test ./...
git add -A
git commit -m "Let administrators mail a password reset link"
```

---

## Task 12: Decision record and documentation

**Files:**
- Create: `docs/decisions/0027-adaptive-lifecycle-warnings-and-account-invitations.md`
- Modify: `AGENTS.md`, `ARCHITECTURE.md`, `SECURITY.md`, `ROADMAP.md`
- Modify: the configuration documentation under `deploy/`

- [ ] **Step 1: Write the decision record**

Create `docs/decisions/0027-adaptive-lifecycle-warnings-and-account-invitations.md` with the project's existing ADR shape (`## Status`, `## Decision`, `## Consequences`) — read `docs/decisions/0020-password-reset-and-email-outbox.md` for the house style. It must state:

- Status: Accepted. Amends 0015 and 0020.
- Warning lead times are derived from the timeout window as `min(window / divisor, max)` and suppressed below `min`, so a short stop window sends nothing and a long retention window warns a bounded time ahead. `COWS_EMAIL_WARNING_LEAD_TIME` is removed and rejected at startup.
- A deletion the owner did not perform queues an advisory notice naming the workspace and reason only.
- One outbox table with a nullable dedupe key replaces the two previous tables; delivery is a single retrying loop; only lifecycle warnings are re-checked against live state before sending.
- Accounts may be created without a password, in which case the stored hash is unusable and a single-use invitation is mailed. Blank password with no deliverable address is refused.
- Invitation and reset tokens share storage but not purpose; neither is accepted by the other's path.
- Reset requests are throttled per account without changing the response, keeping the endpoint non-enumerating.
- Consequences: email remains advisory and optional; deployments without SMTP keep the temporary-password paths for both creation and administrator recovery.

- [ ] **Step 2: Update AGENTS.md**

Add to the decision-record list, matching the surrounding sentence style:

```
Adaptive lifecycle warning, workspace deletion notice, account invitation,
and email outbox changes must follow
`docs/decisions/0027-adaptive-lifecycle-warnings-and-account-invitations.md`.
```

In the email paragraph of "Security rules", replace the single-lead-time description with the window-relative policy and add that account invitations store an unusable password hash and that invitation and reset tokens are not interchangeable.

- [ ] **Step 3: Update ARCHITECTURE.md**

In the notification-worker description, record that there is one outbox table and one delivery loop, that warning lead times are derived from each workspace's own timeout window, and that only the two warning kinds are re-validated against live state before delivery.

- [ ] **Step 4: Update SECURITY.md**

Add: invitation tokens are hashed, single-use, purpose-scoped, and expire; invited accounts hold an unusable password hash rather than a shared placeholder; reset requests are throttled per account with an unchanged response; deletion notices carry no volume name, host path, runtime identifier, or archive location.

- [ ] **Step 5: Update ROADMAP.md**

Move the delivered items into the implemented list for the email milestone and record the remaining deferred work (email verification, institutional identity) unchanged.

- [ ] **Step 6: Update the deployment configuration documentation**

Document the added keys — `COWS_EMAIL_WARNING_LEAD_DIVISOR`, `COWS_EMAIL_WARNING_LEAD_MAX`, `COWS_EMAIL_WARNING_LEAD_MIN`, `COWS_INVITATION_LIFETIME`, `COWS_PASSWORD_RESET_LIFETIME`, `COWS_PASSWORD_RESET_MIN_INTERVAL` — with their defaults, and state plainly that `COWS_EMAIL_WARNING_LEAD_TIME` was removed and now fails startup. Note that invitations require both email delivery and `COWS_EXTERNAL_BASE_URL`.

- [ ] **Step 7: Full verification and commit**

```bash
tools/web-assets.sh verify
gofmt -l $(git ls-files '*.go') && go vet ./... && go test ./...
go build -o bin/cows ./cmd/cows
git add -A
git commit -m "Record the lifecycle warning and invitation decision"
```

---

## Self-Review Notes

**Spec coverage.** Adaptive lead → Task 3. Configurability → Task 3. Deletion notice for timeout and administrator deletions → Task 4. Unified outbox → Tasks 1-2. Invitation on admin creation → Task 6. Invitation accept → Task 7. CSV import → Task 8. Self-registration welcome → Task 9. Reset lifetime, throttling, discoverability → Task 10. Administrator-triggered reset → Task 11. Documentation and ADR → Task 12.

**Two corrections made while writing.**

Task 4 originally placed `WorkspaceDeletedMessage` in `internal/notifications`. That is an import cycle: `internal/notifications` already imports `internal/workspace` for `EvaluateTimeouts`. The builder lives in `internal/workspace/notice.go` instead, and the workspace service enqueues through the store it already holds rather than taking a notifications dependency.

Task 10 originally added a "Forgot your password?" link to the login page. That link already exists at `web/templates/pages/login.html:17`, correctly gated on `PasswordResetEnabled`, which `cmd/cows/main.go:116` already derives from email being configured. The step is a regression test, not a change.

**Ordering constraint.** In Task 4, the deletion notice must be enqueued *after* `CancelEmailMessagesForWorkspace`, which cancels every pending message whose dedupe key starts with `workspace:<id>:`. Enqueueing first would cancel the notice immediately.

**Test that must be updated, not deleted.** `TestTimeoutWarningsAreDeduplicatedAndDelivered` uses a one-hour stop window, which the new policy suppresses. Task 2 Step 8 changes it to a six-hour window so it still exercises the delivery path it was written for.
