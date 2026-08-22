# ADR 0027: Adaptive Lifecycle Warnings and Account Invitations

## Status

Accepted. Amends `0015-email-notifications.md` and
`0020-password-reset-and-email-outbox.md`.

## Decision

Lifecycle warning lead times are derived from each workspace's own timeout
window rather than from a single configured duration. The lead is
`min(window / divisor, max)`, and no warning is sent when that falls below a
floor. At `max * divisor` the two bounds meet, so the function is continuous.
With the defaults, a one-hour no-connection stop window sends nothing, a
six-hour window warns two hours ahead, and any retention window of three days
or more warns 24 hours ahead. Suppressing the short case is intentional: an
automatic stop is reversible, and a notice too late to act on is noise.
`COWS_EMAIL_WARNING_LEAD_TIME` is removed and rejected at startup rather than
accepted and ignored.

A deletion the owner did not perform queues an advisory notice: the automatic
retention-expiry deletion and an administrator deleting someone else's
workspace. An owner deleting their own workspace is not mailed about it. The
body names the workspace, the time, the reason, and whether storage was
retained, and carries no volume name, host path, runtime identifier, or
archive location. The notice is queued after the cancellation that clears the
workspace's pending mail, and its failure never fails the deletion.

One `email_messages` table replaces the two previous outbox tables. A nullable
`dedupe_key` under a partial unique index gives lifecycle warnings their
per-workspace deduplication while leaving one-shot mail unconstrained.
Re-enqueueing an identical warning does not resend it; a changed body, which
encodes the deadline, is a different statement and is delivered. Delivery is a
single retrying loop over one sender interface. Only the two lifecycle warning
kinds are re-validated against live state before sending; every other kind
describes something that already happened and is always current. Message
identity is not a foreign key to the user row, so a deletion notice survives
the account it concerns.

Accounts may be created without a password. Such an account stores a bcrypt
hash of a random value that is immediately discarded, so nothing can
authenticate against it, and is opened only through a mailed single-use
invitation that sets the first password, clears the forced password change,
and invalidates every session. This is deliberately not an empty or sentinel
hash: either would be a guessable secret shared across every invited account.
A blank password with no address, or with email delivery disabled, is refused.
Deployments without SMTP keep the existing temporary-password paths for
creation, import, and administrator recovery unchanged.

Invitation and reset tokens share storage and their security properties —
hashed, single-use, expiring — but carry an explicit purpose, and neither
path accepts the other's token. Replacing a token is scoped to its own
purpose, so issuing an invitation does not void a reset in flight, and any
queued message carrying a superseded link is canceled.

Reset requests are throttled per account. The response is byte-identical
whether or not the throttle applied, so the endpoint stays non-enumerating.
The administrator-triggered reset is deliberately not throttled and names its
refusal reason: the caller is authenticated and already sees the account.

## Security consequences

No message body contains a password, session secret, token beyond its own
link, runtime identifier, host path, volume name, or user content. Link
construction refuses a base URL carrying a query or fragment, so a token
cannot be smuggled into an existing parameter.

Email remains advisory and optional. Enqueueing is best-effort at every call
site: no lifecycle operation, account creation, import, or deletion is rolled
back or reported as failed because mail could not be queued or sent.

Invitations and reset links require both email delivery and a configured
external base URL. Without them the features are inert rather than
half-enabled.

Email verification and institutional identity provisioning remain out of
scope. The self-registration welcome message carries no token and is not
address verification.
