# ADR 0014: Self-registration and account credentials

## Status

Accepted for the local-account milestone.

## Decision

COWS supports an administrator-controlled self-registration endpoint, disabled
by default. When enabled, a new registrant submits a username, display name,
email address, password, and password confirmation. The server assigns the
user role and applies configured default quota values and default group names;
the browser cannot select or override any of those values.

Registration is an ordinary local account creation flow, not an invitation or
email-verification flow. The account receives an active session only after a
successful login, not automatically after registration. Registration is
rate-limited per source in the single-process deployment. The initial design
does not claim that an email address is verified. Institutions requiring
verified identity must keep registration disabled until a separately reviewed
verification or institutional-authentication milestone is implemented.

Administrator-created users continue to receive temporary credentials and are
required to change the password at first login. Authenticated users can change
their password through the account page by providing the current password and
the new password twice. Password reset by email is deliberately not included;
it requires a separate short-lived, single-use token design and audit model.

The SQLite registration operation inserts the user, the configured user quota,
and the resolved group memberships in one transaction. If a configured default
group does not exist or any insert fails, no partial account is created.

## Consequences

The feature needs no new runtime dependency or frontend build step. It is
appropriate for a single-server deployment, but its process-local rate limit
must move to an upstream or shared mechanism before multiple active COWS
instances are supported. Operators must treat self-registration as an open
account-creation policy and configure conservative quotas.
