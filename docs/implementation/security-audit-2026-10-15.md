# Security audit and fix disposition — 2026-10-15

The implementation-first audit reviewed COWS at
`63fb691e8bb80e0e00adc135e294883123ff1cd7`, then compared the results with
`security-review-2026-09-10.md` and the tracked implementation audit. It ranked
19 findings: five high, twelve medium, and two low. Fourteen isolated tests
reproduced vulnerable behaviors before fixes. Those reproductions asserted the
bad behavior; the repository regression tests now assert the corrected behavior.

The follow-up fixes the fourteen concrete implementation defects and leaves five
accepted design risks unchanged, as requested. Entry 14 was initially labeled a
design concern; bounded directory enumeration repairs the existing traversal
limit contract without introducing disk quotas, a helper resource policy, or a
new global concurrency policy.

## Ranked findings

| # | Severity | Finding | Disposition and regression evidence |
| --- | --- | --- | --- |
| 1 | High | CSV email corrections leave links mailed to the old address usable | Fixed. Import detects a changed address and retires both token purposes and credential mail in its transaction. `auth/credential_concurrency_test.go`, `sqlite/credential_test.go`. |
| 2 | High | Overlapping password verification or address lookup can issue sessions/links after revocation | Fixed. Monotonic credential generations guard issuance; password changes and revocations are atomic; delayed mail enqueue checks current token and recipient. Concurrency, ABA, and injected rollback tests in the same two files. |
| 3 | High | Admission/workspace lock inversion can block other users | Fixed. Cancelable workspace-before-admission order, state reloads under locks, stale observation guards. `workspace/concurrency_test.go`. |
| 4 | High | Lifecycle multipart parsing precedes the body cap | Fixed. The 1 MiB cap is installed before parsing/classification; parse errors prevent the operation. Counting-body regression in `web/security_hardening_test.go`. |
| 5 | High, design | Measured storage allowances do not contain tenant byte/inode/log/retained-data growth | Unchanged. ADR 0012's soft storage policy remains. Host filesystem headroom and hard containment need a separate decision. |
| 6 | Medium, conditional | Disconnect cleanup executes a writable configured shell as container root | Fixed. Direct trusted `/bin/kill`, inherited environment cleared, protected system mounts rejected, managed PID rechecked. Unit and opt-in `podman/live_terminal_test.go` exercise writable shell/startup files and a TERM-ignoring child. |
| 7 | Medium | Login check-then-act admits parallel attempts above the cap | Fixed. Atomic reservations count in-flight work; completion cannot erase another active reservation. `auth/limiter_test.go`. |
| 8 | Medium, design | Direct-peer IP limits cause shared proxy lockouts and lack account-level login policy | Unchanged. No forwarded-header trust, account lockout policy, or shared limiter was introduced. |
| 9 | Medium | Public confirmation bcrypt work is unthrottled; successful registrations reset their budget | Fixed. Shared source confirmation budget precedes password work; registration successes consume attempts. `web/security_hardening_test.go`. |
| 10 | Medium, conditional | Attacker-injected matching CSRF cookie/form values authorize mutations | Fixed. Session-bound signed cookies plus Origin/fetch-metadata checks; HttpOnly CSRF cookies. Injected/other-session/sibling-origin regressions in `web/security_hardening_test.go`. |
| 11 | Medium | Successful reattachment forgets the actual named-volume source | Fixed for new reattachments. Migration 0031 persists source identity; file access, usage, recreation, and tombstones reuse it. `workspace/reattach_test.go`. Already-lost historic identities require manual recovery. |
| 12 | Medium | Persisted phantom connections suppress idle timeout after restart | Fixed. Startup resets process-owned counts, preserves existing idle deadlines, and grants lost connections a fresh idle window. Stream Close is once-only and running-generation-bound. `workspace/concurrency_test.go`. |
| 13 | Medium | Concurrent account writes bypass application-only email uniqueness | Fixed. Database-enforced normalized non-empty uniqueness and public conflict mapping; duplicate-data migration fails without modifying accounts. Unicode case variants, concurrent registration, safe duplicate rollback, and ordinary SQLite VACUUM are covered by auth/database/SQLite tests. |
| 14 | Medium | WalkDir allocates a whole directory before callback entry limits | Fixed. Rooted archive/usage enumeration reads at most 128 entries per batch and at most the remaining budget plus one. Skipped special entries also consume scan budget. `archive/walk_test.go`, `archive/archive_test.go`, `fileagent/helper_test.go`. |
| 15 | Medium | HTTPS external URLs allow insecure cookies and public HTTP credential links | Fixed. Startup checks URL/cookie combinations; explicit loopback HTTP development remains supported. `config/config_test.go`. |
| 16 | Medium, design | Desktop isolation is optional and shared bridges can bypass application gateway checks | Unchanged. ADR 0023 defaults, credential policy, and existing-container migration are not altered. |
| 17 | Medium, design | Interrupted/uncertain runtime outcomes lack complete non-destructive repair | Unchanged. Orphans are observed, not automatically deleted; image preparation/store-lock stalls and failed reattachment consumption remain operator concerns under existing decisions. |
| 18 | Low, design | Outbox bodies retain raw credential links after send/cancellation | Unchanged in storage behavior. SECURITY.md now explicitly includes sent/canceled bodies and backups in this credential boundary. |
| 19 | Low | SMTP protocol I/O can block the delivery worker after context expiry | Fixed. Total 30-second deadline plus cancellation-driven connection closure covers greeting through QUIT. Stalled-greeting and stalled-command tests in `notifications/smtp_test.go`. |

Test paths above are under `internal/`. Accepted-boundary amendments are recorded
in [ADR 0029](../decisions/0029-security-audit-hardening.md).

## Comparison with the earlier review

- F1/F2/F4/F15 correspond to ranked fixes 1/7/15/10. The September 30 tracked
  closure claim for F1 was incorrect: the actual CSV update still omitted
  invalidation. It is now covered by redemption and queued-mail regressions.
- F3/F7 are retained design risks 8/18. F14's short-lived, administrator-bound
  password CSV export remains the accepted sensitive-export mechanism.
- F5 is hardened: bootstrap/SMTP password CLI flags are removed; protected
  environment configuration remains available. Configuration tests reject the
  old flags without echoing values.
- F6 had no established reachable header injection, but the SMTP sink now
  validates exact mailbox addresses and rejects CR/LF in subjects before dial.
- F8 had no demonstrated purpose bypass: select and consume were already in one
  transaction. Consume now repeats purpose/expiry predicates as defense in depth.
- F9's retained-volume deletion remains administrator-gated. The repository
  interface now explicitly marks its unscoped primitive administrator-only;
  user routes keep their separate owner-scoped methods.
- F10 remains a defense-in-depth concern for a writer tampering with trusted DB
  archive metadata, rather than a demonstrated browser escape. It is not treated
  as a newly fixed authorization boundary.
- F11's UID-0 file helper is intentionally used for server-selected usage and
  retained-volume recovery. Rejecting it globally would break accepted flows;
  this was not conflated with terminal cleanup issue 6. A 2026-10-26 follow-up
  (ADR 0030) keeps those flows but makes namespace-root access explicit,
  validates the rootless maps, and verifies the helper's namespace identity;
  configured container users always drop to a nonzero mapped identity.
- F12's final symlinked listing base is now rejected by both local access and the
  helper, consistently with downloads. Rooted confinement remains authoritative;
  this pre-check is not a claim of race-proof exclusion of every in-root symlink.
- F13 is hardened with an enforced CSP and removal of the remaining native inline
  file-select handler. The current Alpine/client compatibility allowances are
  documented; no new frontend build or dependency was introduced.
- F16's raw filenames were escaped in the inspected rendering paths; no filename
  XSS was established, so no speculative escaping rewrite was made.
- A1 administrator recovery and A3 runtime-aware readiness were implemented
  already. Stale overview claims of missing offline recovery are corrected.
  A2's actual backup/restore drill remains incomplete.
- B1/B2 retain broader repair gaps despite concrete fixes 3/11/12. B3/B4's
  one-active-process coordination remains intentional. C1's old heading was
  stale: opt-in Podman tests already existed and now include adversarial terminal
  cleanup. C2's broader real-namespace symlink-race campaign remains future work.
- Existing orphan observations still create repeated audit entries. This remains
  part of operational audit-growth/repair work, not a claimed container escape.

## Upgrade and operational limits

Existing duplicate email addresses refuse migration transactionally. Resolve
ambiguous account data explicitly and retry; migration never chooses an account
or deletes user data. Previous sessions and credential links are retired on upgrade because their
original generation/recipient was not stored; historically stale sessions or
CSV-issued links cannot be distinguished safely. Users sign in again and
request/reissue links after upgrading. Account passwords and lifecycle mail are
unaffected.

Historic successful volume reattachments may already have lost their source
identity. Migration preserves ordinary legacy naming but cannot reconstruct that
history. Recover those workspaces before deleting or recreating them.

Approved images must provide trusted `/bin/kill`, its loader, and libraries,
without user-writable aliases. Existing mounts that cover those paths fail
cleanup closed and require operator review. Disconnect cleanup targets the exec
PID/process group; independently created process groups are outside its scope.
Only the tested Podman/image combination below has live evidence.

CSP still permits Alpine's `unsafe-eval` and client inline styles. CSRF forms
rendered before restart/sign-in must be reloaded. No disk containment, new proxy
trust, mandatory network isolation, encrypted outbox, or automatic runtime repair
was introduced.

## Validation

Final unit tests, `go vet ./...`, `go build -o bin/cows ./cmd/cows`, source
formatting, `git diff --check`, and `tools/web-assets.sh verify` passed. The final
`go test -race ./...` also passed, including concurrent credential, lifecycle,
filesystem, SMTP, and WebSocket regressions. The slowest race package completed
in 189.694 seconds.

Firefox 153.0 with installed Playwright 1.62.1 passed the temporary browser fixture
over HTTP and HTTPS termination to an HTTP backend. It exercised authenticated
Alpine navigation/template expressions, HTMX fragments and signed-CSRF mutations,
xterm terminal WebSockets, noVNC module/canvas/RFB-handshake WebSockets, and file
mount navigation. Positive pages produced no CSP violations or page errors;
inline script elements, native handlers, and foreign fetches were blocked.
These browser tests used fake runtime streams, not a live desktop. Results and
replay harness are under `/tmp/cows-csp-smoke/`; missing GTK dependencies were
extracted under `/tmp`, with no host installation or repository dependency.
Chromium/WebKit were not tested. IPv6 could not bind on the host, so its CSP
`self` fallback remains unverified.

Live opt-in Podman suite passed on rootless Podman 6.1.2 with local
`docker.io/library/ubuntu:26.04`, image digest
`sha256:c6c0067e0e45b7a826eaebb193cef957be28045380963a9b1eeb2a5d3c70a1b9`.
It covered network isolation on both create paths, identity/mount mapping,
file-helper upload, and the adversarial terminal cleanup test. The complete
package completed in 24.950 seconds. Test-owned data/containers were temporary.
Podman 4.1 was not live-tested.

The final `govulncheck` v1.9.0 scan found zero reachable Go dependency
vulnerabilities and zero affected imported packages. Four unused x/crypto
SSH/OpenPGP module advisories were not counted as reachable COWS vulnerabilities.
The audit did not prove absence of
host/container escapes or independently audit vendored third-party code.

Production reverse-proxy operation, hostile-workload host isolation, load
exhaustion, real SMTP delivery, and actual backup/restore remain unverified.
COWS remains suitable for development/trusted trials, not an assertion of
production readiness for hostile tenants.
