# ADR 0015: Email notifications

## Status

Accepted for the initial notification implementation.

## Decision

Email delivery is optional. COWS uses the Go standard library SMTP client and
does not add a mail framework or frontend dependency. SMTP configuration is
provided by the server operator, including host, port, sender, and optional
authentication credentials. Credentials are never rendered, logged, or
stored in the SQLite database.

Lifecycle warnings are created from server-side timeout state. The warning
worker persists a deduplicated notification event before attempting delivery,
and delivery status, retry count, next-attempt time, and a sanitized error
category are stored in SQLite. Sending is separate from reconciliation and
container stop/delete operations. A failed email therefore cannot prevent or
roll back an authoritative lifecycle action.

The first warning policy covers an upcoming automatic stop and an upcoming
automatic container deletion. One configurable lead time is applied to both
deadlines. Notifications are sent only to the workspace owner's stored email
address. Empty addresses, disabled users, missing policy state, and missing
SMTP configuration fail closed without creating a send attempt. Warning
messages contain workspace name, effective deadline, and the action; they do
not contain terminal output, secrets, runtime identifiers, host paths, or
container addresses.

Delivery is best-effort and retryable. Duplicate delivery is possible around a
process crash between SMTP acceptance and status persistence, so messages must
not be treated as authorization or lifecycle guarantees. A future deployment
may replace SMTP with an external provider behind the same internal sender
interface.

## Consequences

The implementation remains a small modular-monolith component with no new
third-party dependency. Operators must configure an authenticated local relay
or trusted SMTP service and protect the process environment or configuration
file containing credentials. Email is an advisory channel; the web interface
and backend timeout worker remain authoritative.
