# ADR 0020: Local Password Reset and Email Outbox

## Status

Accepted

## Decision

COWS supports local-account password reset without institutional identity
providers. Reset requests are non-enumerating. The backend creates a random,
single-use value, stores only its SHA-256 hash, and queues the raw value only
inside a short-lived reset URL in a separate email outbox record.

Consuming a valid value updates the password, clears forced password change,
invalidates all sessions, and records an audit event. Email delivery is
optional, retryable, and never blocks workspace lifecycle operations. Reset
links use the configured external base URL and should only be enabled behind an
HTTPS reverse proxy. Email verification and institutional recovery are out of
scope.

## Security consequences

Reset requests do not disclose account existence. Disabled accounts do not get
usable reset messages. Tokens, passwords, SMTP credentials, and message
contents are never logged or recorded as audit metadata.
