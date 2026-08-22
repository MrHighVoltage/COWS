# Adaptive lifecycle warnings and account invitations

Design for extending COWS email to adaptive timeout warnings, deletion
notices, account invitations, and password-reset refinements.

Amends `docs/decisions/0015-email-notifications.md` and
`docs/decisions/0020-password-reset-and-email-outbox.md`. A new decision
record, `0027-adaptive-lifecycle-warnings-and-account-invitations.md`,
records the accepted boundaries.

## Starting point

The notification subsystem already exists. `internal/notifications` holds a
persisted, deduplicated outbox with bounded retries, an SMTP sender built on
the standard library, a timeout-warning enqueue pass, and password-reset
messages. Delivery is separate from reconciliation, so a failed send cannot
roll back a lifecycle action.

Three gaps motivate this work.

A single `COWS_EMAIL_WARNING_LEAD_TIME`, defaulting to 24 hours, is applied to
both the no-connection stop deadline and the stopped-retention delete
deadline. A stop window is typically about an hour, so a 24-hour lead makes
the stop warning fire immediately or not at all.

Nothing is sent once a workspace is actually deleted. The owner learns about
it by looking.

Account creation sends no mail. An administrator invents a password and
relays it out of band.

## Scope

In scope: adaptive warning lead times, a post-deletion notice, a unified
email outbox, account invitations, and four password-reset refinements.

Out of scope: email verification, institutional identity, and any change to
what the timeout worker itself does. Email remains advisory; the web
interface and the timeout worker remain authoritative.

Configuration-file loading is a separate design, sequenced after this one.
The keys introduced here are ordinary environment variables and carry into
that loader unchanged.

## Adaptive warning lead time

`workspace.EvaluateTimeouts` already returns the phase and the deadline. The
window length is on the workspace record: `InitialConnectionTimeoutSeconds`
for the stop phase and `StoppedRetentionSeconds` for the delete phase. No new
state is required.

A pure function in `internal/notifications` computes the lead:

```
lead(window) = min(window / divisor, max)
```

The warning is suppressed entirely when `lead < min`.

Defaults are `divisor = 3`, `max = 24h`, `min = 1h`. At a three-day window the
two branches meet exactly (72h / 3 = 24h), so the function is continuous.

| Window | Lead | Sent |
| --- | --- | --- |
| 1h stop | 20m | no, below floor |
| 3h stop | 1h | yes |
| 6h stop | 2h | yes |
| 2d retention | 16h | yes |
| 3d retention | 24h | yes |
| 30d retention | 24h | yes |

The floor means an ordinary short stop window produces no mail. That is the
intended outcome. An automatic stop is reversible, and a twenty-minute notice
is noise rather than warning.

Three settings replace `COWS_EMAIL_WARNING_LEAD_TIME`, each with a matching
flag:

- `COWS_EMAIL_WARNING_LEAD_DIVISOR`, default `3`
- `COWS_EMAIL_WARNING_LEAD_MAX`, default `24h`
- `COWS_EMAIL_WARNING_LEAD_MIN`, default `1h`

Configuration validation requires `divisor >= 1`, `min > 0`, `max > 0`, and
`min <= max`.

`COWS_EMAIL_WARNING_LEAD_TIME` is removed rather than accepted and ignored.
An operator who set it deliberately gets a startup error naming the
replacement keys. Silently changing warning behavior under a setting that
still appears to work would be worse than a failed start.

## Deletion notice

A `workspace_deleted` message kind is enqueued after the container deletion
succeeds, for two cases: the automatic stopped-retention deletion performed
by the reconciliation worker, and an administrator deleting someone else's
workspace. A workspace the owner explicitly deleted sends nothing; they just
did it and saw the result.

The dedupe key is `workspace:<id>:deleted`.

Enqueue failures are logged and swallowed. Email must never block or roll back
a lifecycle action, and a deletion that has already happened cannot be undone
because a message could not be queued.

The body names the workspace, the time, and the reason — retention expiry or
administrator action. It mentions that retained storage can be reattached only
when a tombstone was actually persisted. It never contains a volume name, host
path, runtime identifier, or container address.

## Unified outbox

Two near-identical outbox tables already exist. This work adds three message
kinds, which under the current pattern would push the duplication toward four
tables and four delivery loops. They are merged first.

Migration `0028_unified_email_outbox.sql`:

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
```

The partial unique index gives lifecycle warnings their per-workspace
deduplication while leaving one-shot mail unconstrained.

`user_id` is deliberately not a foreign key with cascade. A deletion notice
should survive the user row; the existing `ON DELETE CASCADE` on
`password_reset_emails` would silently discard queued mail.

Rows from `email_notifications` and `password_reset_emails` are copied
forward within the migration transaction, lifecycle rows taking
`dedupe_key = 'workspace:' || workspace_id || ':' || kind`, and both old
tables are dropped.

Message kinds: `timeout_stop`, `timeout_delete`, `workspace_deleted`,
`password_reset`, `invitation`, `welcome`.

`Deliver` collapses to one loop. The `Sender` and `MessageSender` split
disappears in favor of a single `Send(ctx, recipient, subject, body)`
interface. The staleness check in `notificationIsCurrent` becomes a per-kind
hook: only `timeout_stop` and `timeout_delete` have one, and every other kind
delivers unconditionally.

## Account invitations

`password_reset_tokens` gains `purpose TEXT NOT NULL DEFAULT 'reset'`,
constrained to `('reset', 'invitation')`. Both flows share their security
properties — hashed storage, single use, expiry, session invalidation on
consumption — and branch only where behavior genuinely differs: the audit
event and the redirect target.

The invitation lifetime is `COWS_INVITATION_LIFETIME`, default `24h`,
separate from the reset lifetime.

### Administrator user creation

The password field becomes optional. A blank password means COWS stores a
bcrypt hash of a discarded random value, so no password can ever match; sets
`MustChangePassword`; issues an `invitation` token; and queues the message.

A blank password combined with a missing email address, or with email
delivery disabled, is a validation error that names the reason. Creating an
account nobody can reach or log into must fail closed.

A non-blank password keeps today's behavior, for deployments without SMTP.

### Accepting an invitation

`GET /invitation/accept` and `POST /invitation/accept` mirror the
reset-confirm handlers. Consuming a valid token sets the password, clears
`MustChangePassword`, marks the token used, invalidates all sessions for the
user, records a `user.invitation_accepted` audit event, and redirects to
login. An invalid, expired, or already-used token renders the same generic
message.

Administrators get a resend action that expires any outstanding invitation
tokens for the user before issuing a new one.

### Other creation paths

CSV import applies the same rule per row: a blank password column produces an
invitation. The outbox already delivers at most fifty messages per pass, which
is the natural throttle for a bulk import.

Self-registration sends a plain welcome message with no token and no link. It
is informational. ADR 0015's prohibition on adding email verification
incidentally stands.

## Password reset refinements

**Lifetime.** `COWS_PASSWORD_RESET_LIFETIME` replaces the hardcoded one hour,
defaulting to `2h`.

**Rate limiting.** Before queueing a reset, the service checks for a
`password_reset` message for that `user_id` created within
`COWS_PASSWORD_RESET_MIN_INTERVAL`, default `5m`. If one exists, no new
message is queued. The HTTP response is identical either way, so the throttle
does not become an account-existence oracle.

**Discoverability.** The login page carries a "Forgot your password?" link,
rendered only when email delivery is configured. `/password/reset` states
plainly when email is disabled rather than accepting a request that can never
be delivered.

**Administrator-triggered reset.** Alongside the existing temporary-password
recovery, an action issues a `reset` token and mails the link, so no secret is
relayed out of band. The temporary-password recovery stays for deployments
without email.

## Security properties

Tokens are stored only as SHA-256 hashes and are single-use. Invitation and
reset purposes are not interchangeable: a token issued for one purpose is
rejected by the other's consumption path.

An account awaiting an invitation has an unusable password hash rather than a
guessable placeholder or an empty hash.

Reset requests remain non-enumerating, including when throttled.

No message body contains a token beyond its own link, a password, a runtime
identifier, a host path, a volume name, or user content. SMTP credentials are
never logged, rendered, or persisted.

Enqueueing is best-effort at every call site. No lifecycle operation, user
creation, or deletion is rolled back because mail could not be queued or sent.

## Testing

Lead-time computation is table-driven over the boundaries: the exact
three-day crossover, a lead exactly at the floor, a lead just below it, and
zero or negative windows.

Outbox tests cover dedupe-key collision, retry exhaustion moving a message to
`canceled`, and a migration that preserves the rows of both old tables with
correct dedupe keys.

Authentication tests cover an invitation token rejected by the reset path and
the reverse, an expired invitation, an unusable-hash account rejecting every
login attempt, blank password with no email address rejected at creation, and
a throttled reset request returning a response identical to an accepted one.

Web tests cover CSRF protection and unauthenticated access for the two new
routes, and the conditional rendering of the login-page reset link.

Deletion-notice tests confirm a notice for timeout and administrator
deletions, no notice for an owner's own deletion, and that a failing enqueue
does not fail the deletion.

Podman is not required for any of these tests.

## Documentation

New decision record
`docs/decisions/0027-adaptive-lifecycle-warnings-and-account-invitations.md`,
amending 0015 and 0020.

Updates to `AGENTS.md` for the decision-record pointer, `ARCHITECTURE.md` for
the notification worker, `SECURITY.md` for invitation tokens, unusable
password hashes, and reset throttling, `ROADMAP.md` for the milestone, and the
`deploy/` configuration documentation for the changed and added settings.

## Known risks

The outbox migration is the only genuinely risky step. It copies and drops
within a single transaction, but it touches live queued mail.

Removing `COWS_EMAIL_WARNING_LEAD_TIME` is a breaking configuration change.
Deployments that set it will fail to start until the key is replaced. This is
deliberate.
