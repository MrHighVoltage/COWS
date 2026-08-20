# ADR 0026: Offline Administrator Credential Recovery

## Status

Accepted

COWS gains a `cows recover-admin [-database PATH] USERNAME` subcommand. It
resets the named administrator's password to a generated temporary one, sets
the mandatory first-login password-change flag, invalidates every session for
that account, records an `administrator.recovered` audit event, and prints the
password once to stdout. Until now the only way back into a COWS instance whose
administrator credentials were lost was hand-editing password hashes in SQLite,
which `docs/deployment.md` explicitly forbids; Milestone 1's exit criterion
("document recovery for the first administrator and lost credentials") was
unsatisfiable because no mechanism existed to document.

The trust boundary is local access to the database file, not any credential.
The command opens SQLite directly and starts no HTTP listener, no Podman
runtime, and no background loop, so it works on a host whose COWS service is
stopped or whose runtime is broken. Anyone who can read and write the database
can already forge an administrator by other means; the subcommand does not widen
the attack surface, it replaces an error-prone manual edit with an audited,
tested path. There is deliberately no in-application route and no network
exposure.

The command names the exact reason it refused a target — unknown username,
disabled account, or not an administrator. This is the opposite of
`RequestPasswordReset` (decision 0020), which is deliberately vague so the
unauthenticated web endpoint cannot be used to enumerate accounts. Enumeration
is not a threat for a caller who already holds the database file, and a precise
message is the correct operator experience during an outage.

A **disabled** administrator is refused rather than recovered. Re-enabling an
account is a separate, audited decision — often a deliberate offboarding — and
must not happen as a side effect of a password reset. The operator must re-enable
the account first, through the ordinary administrative path.

Existing sessions for the recovered account are dropped via
`DeleteSessionsForUser`. Recovery is used precisely when the account may be
compromised or its state unknown, so a live session must not survive it. This is
stricter than the password-change flow (decision 0014, `RevokeOtherSessions`),
which keeps the caller's own session because the caller proved knowledge of the
current password; the recovery caller has no session to keep.

The temporary password reuses `auth.GenerateTemporaryPassword`, the same
generator as CSV import, and is stored only as a bcrypt hash. It is printed to
stdout, never logged, and never written to the audit event.
