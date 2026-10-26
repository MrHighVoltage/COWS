# COWS security review — verified findings

Date: 2026-09-10. Source: full code audit of the auth/web/repository/file/runtime layers.
Every item below was **re-verified against the current working tree**, not taken from
subagent reports. Line numbers are anchors at review time (worktree may have
unrelated local edits) — search for the quoted symbol when they drift.

Severity scale: **High** (directly exploitable), **Medium** (exploitable under a
realistic condition / meaningful invariant violation), **Low** (latent or
defense-in-depth), **Info** (design note).

Recommended work order: P0, then P1 top-down. Each finding lists evidence,
fix guidance, and tests. Constraint reminders from `AGENTS.md` are in the
"Fixing workflow" section — read them first.

---

## P0 — fix first

### F1. MEDIUM — CSV import email change does not retire outstanding credential tokens

Location: `internal/repository/sqlite/store.go:118` (UPDATE), reached from
`internal/auth/import.go:116-121`.

Evidence:
- `ImportUsers` updates an existing user's email with
  `UPDATE users SET email = CASE WHEN ? <> '' THEN ? ELSE email END, updated_at = ? WHERE id = ?`
  and never calls `invalidateCredentialTokensTx` nor cancels queued `email_messages`.
- Contrast: `UpdateUserProfile` (`store.go:177-205`) calls
  `invalidateCredentialTokensTx` on email change (line 197), with a comment
  stating exactly this invariant: a correction to an address must also take
  away links that were mailed to the old one.

Impact: an administrator fixes a wrong address via CSV import while a password
reset or invitation link is still outstanding and queued for the old address.
The link stays live (tokens are keyed by user, not verified against the current
email), so whoever now reads the old mailbox can take over the account. Violates
the stated security invariant from decision 0014/0020.

Fix: in `ImportUsers`, when the row is `Existing`, compare the current email to
the new one and, if changed, call `invalidateCredentialTokensTx` inside the same
transaction (the function already exists; it deletes both-purpose tokens and
cancels the matching kind of queued mail). Preserve the all-or-nothing batch
transaction.

Tests: extend an import test — existing user with an outstanding reset token +
queued reset message; import row changes the email; assert token row gone, queued
message cancelled (`kind IN ('reset','invitation')`), and that resetting with the
old token after import fails. Follow the transaction assertion patterns in
`internal/repository/sqlite/store_test.go`.

---

### F2. MEDIUM — Login limiter check-then-act race allows a burst past the cap

Location: `internal/auth/limiter.go:41-61`; caller `internal/web/server.go:853-892`
(`loginPost`).

Evidence:
- `Allow(key)` only reads the counter (`return !ok || entry.failures < maxFailures`).
- `Failure(key)` increments it, but it is called only *after* `Authenticate`
  returns an error.
- Concurrent requests (e.g. hundreds of parallel POSTs) all pass `Allow` before
  the first `Failure` lands, so the 5-failure/15-minute cap limits requests only
  after the burst. An attacker gets `min(concurrency, CPU-bound)` guesses per
  window instead of 5. Bounded in practice by bcrypt-default-cost CPU, but the
  design weakness is real.

Fix: make allowance atomic — in `Allow`, lock and increment the failure counter
immediately (reserve a guess), or switch to a token-bucket/counting scheme where
each attempt is decremented before authentication. Simplest correct shape:
`Allow` becomes `Reserve(key) bool` that increments under the mutex; keep
`Failure`/`Success` for window semantics (success resets, failure keeps).
`RetryAfter` must still reflect the reserved attempts.

Tests: `internal/auth/limiter_test.go` — spawn N goroutines calling
Allow+Failure with a limit of 5; assert the number of `true` allowances across a
burst is bounded by 5 (today it is unbounded). Web-level test in
`internal/web/server_test.go`: burst of parallel login POSTs, assert a
`429` appears while total allowed attempts stay capped.

---

### F3. MEDIUM — Rate limiting is IP-only; behind a reverse proxy this is a global lockout DoS, and distributed brute force is unthrottled

Location: `internal/web/server.go:4706-4715`
(`loginRateLimitKey`), `internal/auth/limiter.go` (single process-local map).

Evidence:
- Key is `r.RemoteAddr` only; `X-Forwarded-For` is never consulted.
- COWS is documented to run behind an HTTPS reverse proxy (nginx/Apache/Caddy
  examples in `docs/`). Behind any proxy every user shares the proxy's IP, so
  5 failed logins from anyone lock out **every** user for 15 minutes — an
  unauthenticated DoS.
- There is no per-account counter, so a distributed or IPv6-rotating brute force
  against a single account is entirely unthrottled. (Password reset does the
  right thing per-account: `service.go:267-273`; login does not.)

Fix (needs a decision record — policy change, see Flat-fix guidance):
1. Per-account throttle keyed by normalized username in addition to IP, keeping
   the response non-enumerating (do not reveal whether the account exists).
2. Add a trusted-proxy model: only when configured (`COWS_TRUST_PROXY_HEADERS`)
   use the rightmost `X-Forwarded-For` hop; otherwise `RemoteAddr`. Never trust
   XFF by default.
3. Consider a small shared (on-disk/sqlite) counter so the limit survives a
   restart; current process-local design is documented as intentional (see
   `docs/implementation/audit-findings.md` B4) — do not silently change that
   without a decision record.

Tests: web tests asserting the proxy-IP lockout flow and that per-account buckets
bound login attempts to the same account from many IPs; keep the existing
spoof-proof behavior when the proxy header flag is off.

---

## P1 — deployment hardening

### F4. LOW/MEDIUM — `CookieSecure` defaults false and nothing cross-checks it against an HTTPS external base URL

Location: `internal/config/config.go:74` (`COWS_COOKIE_SECURE` defaults
`"false"`), consume site `internal/web/server.go:4367-4374` (`setSessionCookie` /
`clearSessionCookie`) and `server.go:4327` (CSRF cookie).

Impact: behind the TLS proxy, a forgotten `COWS_COOKIE_SECURE=true` sends the
session and CSRF cookies unencrypted on any non-loopback bind. Default listen is
`127.0.0.1:8080` (`config.go:62`) so real-world impact is low, but the missing
cross-check is a foot-gun.

Fix (config, no decision record needed): when the parsed config has
`CookieSecure == false` and `ExternalBaseURL` parses as `https://`, return a
startup configuration error (loud) or at minimum log a warning. Use
`COWS_EXTERNAL_BASE_URL` semantics already validated at `config.go:193-199`.

Tests: config test for the mismatched pair (http `COWS_COOKIE_SECURE` + https
base URL) asserting the error/warning; existing cookie tests must stay green.

---

### F5. LOW — Secrets accepted as CLI flags, visible in `/proc/<pid>/cmdline`

Location: `internal/config/config.go:138` (`--bootstrap-admin-password`),
`config.go:151` (`--smtp-password`).

Impact: arg vector is world-readable on Linux; the values also land in shell
history. Downgraded from the earlier "Medium" because the env vars
(`COWS_BOOTSTRAP_ADMIN_PASSWORD`, `COWS_SMTP_PASSWORD`) remain the documented
path (`config.go:78, 91`).

Fix (small): deprecate the two credential flags — keep parsing to avoid breaking
existing deployments but log a warning when used; or drop them entirely (breaking).
Prefer: remove the two `StringVar`s and let the flags error like any unknown
flag, updating `docs/configuration.md`.

Tests: config test asserting the flags are rejected / warning is emitted; the
env-only path already covered.

---

## P2 — defense-in-depth (batched hardening, low risk)

### F6. LOW — SMTP send sinks use an unvalidated recipient

Location: `internal/notifications/smtp.go:70` (`client.Rcpt(recipient)`),
`smtp.go:77` (raw `"To: " + recipient` header).

Evidence: `From` is validated at construction (`smtp.go:33`); `recipient` is not.
Today every stored address passed `mail.ParseAddress` upstream at creation, which
rejects CR/LF, so it is not exploitable — but the guarantee lives far from the
sink, and any future enqueue site that skips validation reintroduces SMTP-command
and header injection in one place.

Fix: in `Send`, parse with `mail.ParseAddress(recipient)` and require
`parsed.Address == recipient` (mirroring `From`); fail closed otherwise.

Tests: `internal/notifications/service_test.go` — a recipient with `\r\n` or
spaces is rejected by the sender; a valid address round-trips.

---

### F7. LOW — Outbox rows hold raw redeemable credential tokens until delivered

Location: `internal/notifications/service.go:114-120` (message body persisted
verbatim), `internal/domain/email.go`.

Impact: tokens are hashed in `password_reset_tokens`, but while a message is
pending, `email_messages.body` contains the raw one-time link token. Anyone with
DB file read access (or a future SQL read primitive) can redeem them until
expiry/supersession. Accepted by decision 0020's design; should be explicit in
`SECURITY.md`.

Fix (documentation, not code — decision 0020 scope): add a paragraph to
`SECURITY.md` stating pending outbox bodies contain live single-use link tokens
until delivery/cancellation, and that `email_messages` must stay inside the same
file-permission boundary as the tokens table. Optionally (bigger change, needs a
decision record): store only the token hash in the body and render the URL at
send time.

---

### F8. LOW — Token-consume UPDATE omits the purpose/expiry predicates present in the SELECT

Location: `internal/repository/sqlite/store.go:280`:

```sql
UPDATE password_reset_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL
```

The preceding SELECT (line 266) enforces `purpose = ? AND expires_at > ?`, and
`token_hash` is the primary key, so this is safe today — but the predicate sets
have drifted apart. If hash reuse across purposes ever became possible, or this
statement gets copied elsewhere, it is the weak link.

Fix: add `AND purpose = ? AND expires_at > ?` to the UPDATE (bind the same
values as the SELECT). Purely defensive; no behavior change.

Tests: existing reset/invitation tests must pass unchanged.

---

### F9. LOW — `DeleteRetainedWorkspaceVolume` is an owner-blind delete primitive

Location: `internal/repository/sqlite/store.go:954-955`
(`DELETE FROM retained_workspace_volumes WHERE volume_name = ?`); interface
`internal/repository/repository.go:124`.

Evidence: sole call site is `adminVolumeDelete`
(`internal/web/server.go:3452-3472`) which is admin-gated via `adminForm`, so it
is safe as wired — but nothing on the `Store` interface marks it
administrator-only, unlike the directory counterparts (decision 0022/0025 keep
self-service and admin recovery on separate route namespaces and must never
share primitives).

Fix (no behavior change): add an explicitsafety comment to the interface method
("administrator-only; do not call from user-scoped handlers") and/or mirror the
pattern of `ConsumeRetainedWorkspaceVolume` by adding an owner-scoped variant a
self-service caller would have to use. Update `repository.go`.

---

### F10. LOW — File-agent `restoreDirectoryMounts` trusts DB-stored path strings without rooted confinement

Location: `internal/workspace/mounts.go:288-320` (`directoryMove` source built
from tombstone JSON `NamePrefix + Name + NameSuffix` + `archivedContainerPath`,
then plain `os.Lstat`/`os.Remove`/`os.Rename`). No `os.Root`, no re-validation
of name parts here.

Evidence: today every value is server-generated (name parts validated by
`mountNamePartPattern`, `configuration.go:100-105`), so exploitation requires DB
write access — latent. This is the one filesystem-mutation path in the audit
scope not behind an `os.Root` handle.

Fix (defense-in-depth): re-validate `archivedEntryName` against
`mountNamePartPattern` on restore and/or recompute the archive path from
`s.mountArchiveRoot` + `managedContainerName(directory.WorkspaceID)` instead of
the stored `ArchivePath`, and perform the moves against `os.OpenRoot(...)`.

Tests: add a case where a tampered tombstone `NamePrefix` contains `/` or `..` —
must fail with `ErrMountUnavailable`, never touch paths outside the archive root.

---

### F11. LOW — File-helper accepts `--uid 0` (full identity under `podman unshare`) and has no caller/root allowlist

Location: `internal/fileagent/helper.go:132-145` (`validateOptions` permits UID 0),
`internal/fileagent/identity_linux.go:7-17` (`dropToIdentity` early-returns when
uid/gid already match, leaving the process as userns-root = full COWS host-user
identity), launch site `internal/runtime/podman/file_access.go:77`.

Evidence: `ContainerUID` is always template-resolved server-side today and never
browser-influenced, so this is latent. But a single future bug populating
`FileAccessSpec.ContainerUID` becomes full read/write of COWS-owned data. The
helper also performs no caller identification (subprocess, so `SO_PEERCRED` is
not directly applicable) and no `--root` allowlist.

Fix options (pick one; keep rootless semantics intact):
1. Reject `--uid 0` / `--gid 0` unless an explicit opt-in flag is passed by the
   server (mirrors the terminal UID-0 explicit-administrator-intent rule); or
2. Have the helper verify the root is beneath `COWS_MOUNT_ARCHIVE_ROOT`/mount
   roots when uid==0, and refuse uid 0 combined with an unapproved root.
Tests: `internal/fileagent/helper_test.go` — `--uid 0` refused for a
non-approved root; valid template-resolved UIDs still pass.

---

### F12. LOW — `List` follows an in-root symlinked base path while download of the same path is rejected

Location: `internal/files/service.go:129` (reads `relativePath` without an `Lstat`
symlink pre-check; entries at 135 are rejected), vs `service.go:183-189`
(download Lstats and rejects a symlinked path). Same asymmetry in the runtime
helper respecting list.

Impact: `os.Root` confines any escape, so this is a policy wart, not an escape —
but listing and downloading a given path disagree.

Fix: add the symlink `Lstat` rejection to the list path before `readDirectory`,
mirroring download. Keep the rejection (do not allow-follow).

Tests: extend `internal/files/service_test.go` — list of a symlinked path
returns `ErrInvalidPath`.

---

### F13. LOW — No Content-Security-Policy header

Location: `internal/web/server.go:676-684` (`securityHeaders` sets
`X-Content-Type-Options`, `X-Frame-Options: DENY`, `Referrer-Policy`, and
`Permissions-Policy`; no CSP).

Impact: mitigation-in-depth against injected markup. Assets are local and pinned
(`web/static/`, no CDNs), so a CSP can be strict:
`default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:;
connect-src 'self' ws: wss:; frame-ancestors 'none'; base-uri 'self';
form-action 'self'`. Verify HTMX/Alpine inline handlers (there are none known)
and the WebSocket origins (`ws:`/`wss:` to self) before shipping.

Tests: extend the existing security-headers test
(`internal/web/server_test.go:408`) asserting the CSP header.

---

## Info — document, don't necessarily change

### F14. INFO — Admin CSV export carries plaintext generated passwords

Location: `internal/web/user_import.go:325-348`.

By design (relay-by-hand when email is unavailable), but bulk credentials transit
as plaintext through an HTTP response and the admin's machine, held in memory for
the download TTL (10 min) scoped to the admin's ID. Add a sentence to `SECURITY.md`
describing this and recommending download-and-load-quickly / no-store handling
(already `no-store`).

### F15. INFO — CSRF cookie is not HttpOnly

`internal/web/server.go:4327`. The CSRF value must be readable server-side only;
it does not need to be readable by JS, so `HttpOnly` would be strictly better.
Low value; the double-submit cookie is not session-bound (standard limitation,
mitigated by `SameSite=Lax`). Adding HttpOnly is a one-line hardening.

### F16. INFO — `List` conversion helper `readDirectory` returns raw names

Non-issue; `html/template` contextual escaping covers every render path.
`innerHTML = await response.text()` fragment loading (`web/static/js/access.js:41,49`)
is safe as long as templates keep using `html/template` output.

---

## Already verified solid (do not re-review these)

- Sessions: 32-byte random token, only SHA-256 hash stored
  (`CreateSession` store.go:462-470), expiry + `disabled = 0` enforced in SQL at
  lookup (`FindSessionUser` store.go:472-483), hash-PK index lookup.
- Reset/invitation tokens: purpose-scoped in SQL + CHECK constraint, atomic
  single-use, expiry in SQL, disabled accounts non-enumerating, supersede + mail
  cancellation (store.go:257-301, 1133-1158).
- CSRF: double-submit, constant-time compare after `ParseForm`, `SameSite=Lax`,
  present on every POST route checked.
- WebSocket: `coder/websocket` v1.8.12 default rejects cross-origin handshakes
  (`accept.go:206-233`); terminal/desktop revalidate the session (incl. disabled /
  must-change-password) every 30s.
- Authorization: all 91 routes dispatch through `requireUser` /
  `requireAdministrator` / `adminForm`; tombstone lookups owner-scoped in SQL.
- File access: path traversal defended by 3 layers (service `cleanRelativePath`,
  `os.Root`/openat2, re-validation in the helper); symlinks rejected; uploads
  128 MiB, ZIP streaming bounded (4 GiB / 100k entries), listings capped.
- No SQL injection (fully parameterized; only constant fragments concatenated).
- No secrets or raw runtime errors rendered to ordinary users.
- VNC: loopback mapping verified, credentials `no-store`, never in URLs/logs.

---

## Fixing workflow (constraints from AGENTS.md — read before starting)

1. Preserve unrelated dirty-worktree changes (worktree currently has local edits
   in `internal/auth/*`, `internal/repository/*`, `AGENTS.md`, `SECURITY.md`).
2. Before changing behavior, read `PROJECT.md`, `ARCHITECTURE.md`, `SECURITY.md`,
   `ROADMAP.md`, the decision records, and `docs/implementation/audit-findings.md`
   (mentions B4 rate-limiting as intentional process-local design).
3. **Decision record required before changing an accepted security or
   operational boundary.** Candidates:
   - F3 (proxy-trusted headers, per-account throttle, shared limiter) — yes.
   - F7 (outbox token storage redesign) — yes.
   - F11 (UID-0 policy change) — yes.
   - F1/F2/F4/F6/F8/F9/F10/F12/F13/F15 are bug fixes / config hardening — no new
     decision record, but update `ROADMAP.md` and the relevant architecture or
     security doc in the same change when closing them.
4. Tests: table-driven, `httptest` where appropriate, timeout/authorization/
   irreversible-operation boundaries. Podman must not be required by `go test ./...`;
   live tests opt-in behind a build tag in `.cows-test/`.
5. When an audit finding is closed, update `ROADMAP.md` and
   `docs/implementation/audit-findings.md` in the same commit.
6. Standard checks:

   ```sh
   tools/web-assets.sh verify
   gofmt -w $(rg --files -g '*.go')
   go test ./...
   go vet ./...
   go build -o bin/cows ./cmd/cows
   ```