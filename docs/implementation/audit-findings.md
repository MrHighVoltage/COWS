# Audit findings — gaps between plans and implementation

Audience: LLM coding agents picking up fix work. This document is
self-contained; read it together with `AGENTS.md` and the decision records it
names before changing any behavior.

Audit date: 2026-08-04, at commit `e25fed0` on `main` (6 commits ahead of
`origin/main`). At audit time `go build ./...`, `go test ./...`, and
`tools/web-assets.sh verify` were green. Every finding below was verified
against the code, not just taken from `ROADMAP.md` claims. Line numbers are
anchors at the audit commit and will drift — search for the symbol quoted next
to them.

## How to work from this file

- Sections A and B are the fix queue, in priority order. Section C lists test
  work. Sections D and E describe things that are **intentional design** — do
  not implement them unless the human explicitly asks; several are hard
  security boundaries, not missing features.
- Each finding has: status, evidence, and guidance. When guidance conflicts
  with `AGENTS.md` or a decision record, the decision record wins and the
  conflict should be raised with the human.
- `ROADMAP.md` exit criteria are the acceptance bar. When you close a finding,
  update `ROADMAP.md` and, if a boundary or data flow changed,
  `ARCHITECTURE.md` / `SECURITY.md` in the same commit. Add a decision record
  (`docs/decisions/NNNN-*.md`, next number: 0029) when behavior or policy
  changes.
- Standard checks after any change:

  ```sh
  tools/web-assets.sh verify
  gofmt -w $(rg --files -g '*.go')
  go test ./...
  go vet ./...
  go build -o bin/cows ./cmd/cows
  ```

- Podman must not be required for the unit-test suite (`AGENTS.md`).
  Integration tests that need a live rootless Podman belong behind a build
  tag and must keep their data in the ignored `.cows-test/` directory.

## A. Fix-first findings (operational risk)

### A1. No administrator credential-recovery mechanism or procedure — RESOLVED

- **Update (2026-08-20):** added `auth.Service.RecoverAdministrator`
  (`internal/auth/service.go`) and the `cows recover-admin` subcommand
  (`cmd/cows/recover.go`, dispatched from `main()` alongside `file-helper`).
  It resets a named administrator to a `GenerateTemporaryPassword` value, sets
  `MustChangePassword`, calls `DeleteSessionsForUser`, records an
  `administrator.recovered` audit event, and prints the password once. Unknown,
  disabled, and non-administrator targets are refused with
  `ErrRecoveryTargetInvalid`. It opens only SQLite — no HTTP server, no Podman.
  Tests: `TestRecoverAdministratorResetsOnlyTheNamedAccount`,
  `TestRecoverAdministratorRefusesInvalidTargets`
  (`internal/auth/service_test.go`),
  `TestRecoverAdminPrintsAWorkingTemporaryPassword`,
  `TestRecoverAdminRejectsBadInvocations` (`cmd/cows/recover_test.go`).
  See decision 0026; `docs/deployment.md`, `docs/configuration.md`,
  `ROADMAP.md`, and `README.md` were updated in the same change.
- Original finding: **ROADMAP M1 exit criterion** ("define recovery procedures for the first
  administrator and lost credentials") is currently **unsatisfiable**: there
  is no recovery mechanism to document.
- Evidence: `cmd/cows/main.go` registers no recovery flag or subcommand (no
  `flag.` usage for recovery at all). `docs/configuration.md:27-29,78-79`
  documents only creation-time bootstrap (`COWS_BOOTSTRAP_ADMIN_USERNAME` /
  `COWS_BOOTSTRAP_ADMIN_PASSWORD`). `docs/deployment.md` has no recovery
  section (headings: Build and run, Reverse-proxy rules, nginx, Apache,
  Caddy).
- Guidance: add an operator-invoked recovery path that requires local
  process/database access (e.g. a `./bin/cows` subcommand that resets a named
  administrator's password to a generated temporary one and sets the
  mandatory first-login change flag, reusing the existing auth/repository
  paths). Follow decision 0014 (account credentials) and decision 0020
  (password reset). Then document the procedure in `docs/deployment.md`.
  Write a decision record: this changes operational security behavior.
- Tests: recovery resets only the named account, invalidates its sessions,
  and refuses unknown/non-admin targets.

### A2. SQLite backup/restore procedure is documented but untested — RESOLVED (partially)

- **Update (2026-08-11):** `docs/deployment.md` gained a "Backup and restore"
  section (`.backup` against WAL mode, tarring the mount/archive roots,
  `PRAGMA integrity_check`/`tar -t` verification, and a restore sequence) in
  the same commit (`8c9d4ba`) that originally recorded this finding as "no
  backup or restore procedure exists anywhere" — that claim was stale the
  moment it was committed. Nothing in `internal/`, `tools/`, or `deploy/`
  automates it; the procedure is manual, copy-pasted-from-docs only.
- Remaining gap: no scripted backup tool, and the procedure has never been
  exercised as an actual restore drill. Retained named volumes are explicitly
  excluded from the file-based backup and remain download-only through
  administrator recovery (decision 0022).
- Guidance: script the documented steps (or verify them work end-to-end by
  hand) and record the drill result. See `internal/database/` for the
  connection settings (WAL, busy timeout) the procedure must respect.

### A3. Readiness endpoint does not check the runtime — RESOLVED

- **Update (2026-08-11):** added `GET /readyz` (`internal/web/server.go`:
  `ready`/`readiness`), a separate route from `GET /healthz`. It checks
  SQLite via the existing `PingContext` and rootless-Podman connectivity via
  `s.runtime.Name(ctx)` (the adapter's existing version/host-info call — no
  Podman types leak into `internal/web`), both under a five-second
  `context.WithTimeout` bound so a stalled socket cannot hang the probe past
  the adapter's own ten-second client timeout. Returns HTTP 503 with
  `{"status":"degraded", ...}` when either dependency is unavailable, 200
  otherwise. `GET /healthz` is unchanged (liveness plus SQLite only). See
  decision 0024. Tests: `TestReadyEndpointReportsOKWhenDatabaseAndRuntimeAreHealthy`,
  `TestReadyEndpointReportsDegradedWhenRuntimeIsUnset`,
  `TestReadyEndpointReportsDegradedWhenRuntimeNameFails` in
  `internal/web/server_test.go`.
- `ARCHITECTURE.md`, `PROJECT.md`, `SECURITY.md`, `docs/configuration.md`, and
  `ROADMAP.md` were updated in the same change to point supervisor/reverse-proxy
  readiness probes at `/readyz` instead of `/healthz`.

## B. Safety-net findings (harden existing behavior)

### B1. Reconciliation records but never repairs

- By design today: `internal/workspace/workspace.go:1087-1113` records a
  missing managed container as observed state `"missing"` with error category
  `runtime_missing`, and `workspace.go:1147` records orphaned managed
  containers as `runtime.orphaned_container` audit events. No repair path
  exists, and the *repair policy* itself is undefined (ROADMAP M2/M3 exit
  criteria).
- Guidance: **design first, code second.** The deliverable starts as a
  decision record defining what COWS should do for (a) database record
  without container, (b) orphaned managed container, (c) partially-created
  workspace, including what is surfaced where. Keep the current
  non-destructive default as the baseline — automatic removal of orphans is
  prohibited by the current architecture (`ARCHITECTURE.md` reconciliation
  section; `AGENTS.md`: reconciliation context must remain visible). Any
  automatic repair should be administrator-policy gated, audited, and
  covered by restart/reconcile tests (reuse `internal/workspace/lifecycle_test.go`
  patterns).

### B2. Lifecycle operations are not restart-safe across every partial failure

- Operations are persisted (migration `0009_workspace_operations.sql`) and
  the workspace pages poll operation status, but recovery of interrupted
  operations after a process restart is not assured for every partial
  failure (ROADMAP M3 exit criterion: "make lifecycle operations durable and
  restart-safe across every partial failure").
- Guidance: enumerate the failure points of create/start/stop/restart/delete
  (see `internal/workspace/workspace.go` lifecycle methods and their use of
  `s.runtime`), define per-operation recovery on startup, and make runtime
  calls idempotent where Podman allows it. Add failure-injection tests with
  the fake/runtime seams used in `internal/workspace/*_test.go`. This is the
  highest-effort item in section B; coordinate scope with the human before
  starting.

### B3. Admission is a process-wide mutex (single-instance assumption)

- `internal/workspace/template.go:63-66` — one `admissionMu sync.Mutex`
  process-wide; `internal/workspace/workspace.go:1075-1076` wraps admission.
  Correct for the single-active-process deployment model, not for multiple
  instances.
- Guidance: **no code change now.** This only becomes work when multi-instance
  is planned (together with B4 and distributed locking). If you touch the
  admission path for another reason, preserve the single-lock semantics and
  the fail-closed-on-unknown-capacity behavior (`ARCHITECTURE.md` quota
  section).

### B4. Rate limiting is process-local

- `internal/auth/limiter.go:14-17`: `LoginLimiter is intentionally
  process-local … a future multi-instance deployment needs a shared limiter
  or an upstream control.` Registration throttling lives elsewhere in
  `internal/web` but is likewise in-memory.
- Guidance: **no code change now** (documented, single-instance). Same
  trigger as B3. If login/reset/registration limits are moved, keep them
  non-enumerating (decision 0020) and never log credential material.

## C. Test-coverage findings

### C1. No rootless-Podman integration suite; adapter coverage is thin

- `internal/runtime/podman/podman.go` (~1190 lines) has only
  `podman_test.go` (~570 lines) of unit tests; no build-tagged integration
  tests exist. ROADMAP M2/M4/M5 hardening all call for rootless-Podman
  integration coverage (runtime behavior, terminal exec against real
  containers, representative VNC images).
- Guidance: add opt-in integration tests behind a build tag (e.g.
  `//go:build podman_integration`), skipped by default, keeping data under
  `.cows-test/`. Start with adapter contract behaviors the fake can't prove:
  label matching, loopback port mapping verification, exec streaming and
  cleanup ordering, isolated network attach/cleanup.
- **Update (2026-10-14):** the first opt-in test exists:
  `internal/runtime/podman/live_podman_test.go` (`//go:build
  podman_integration`, image from `COWS_PODMAN_TEST_IMAGE`, data in a
  temporary directory outside the repository, as `AGENTS.md` allows). It
  covers a configured container user: the applied UID/GID map, the passwd
  entry, directory and named-volume ownership, and file-helper uploads.
  `live_network_test.go` covers isolated network attachment, loopback desktop
  publishing, and cross-workspace isolation on both create paths. Label
  matching, exec streaming, and cleanup ordering are still open.

### C2. Symlink-race coverage is unit-level only

- Unit tests exist (`internal/files/service_test.go`,
  `internal/fileagent/helper_test.go`), but ROADMAP M7 asks for stronger
  integration coverage of the file helper's rooted path handling under
  rootless namespace conditions.
- Guidance: extend C1's tagged suite rather than simulating races in unit
  tests. Do not weaken the rooted-path or per-workspace file-lock design
  (`internal/workspace/file_access.go`) while adding tests.

### C3. Abuse, session-invalidation, and import-failure tests — password-change half RESOLVED

- **Update (2026-08-11):** commit `3ed253a` closed the password-change half.
  `ChangePassword` previously left every other session valid; `auth.Service`
  now has `RevokeOtherSessions` (backed by
  `DeleteSessionsForUserExcept`), called from the password POST handler after
  a successful change, keeping the caller's own session (including the
  mandatory first-login change) and revoking every other session for the
  account. Covered by `internal/auth/service_test.go` (keep-caller,
  revoke-stolen, revoke-all) and `internal/web/server_test.go`
  (`TestPasswordChangeKeepsCallerAndRevokesOtherSessions`). `SECURITY.md` now
  documents this alongside disable-based invalidation.
- **Update (2026-09-30):** the import duplicate-handling half is closed.
  `ImportUsers` now rejects a batch whose rows repeat an email address or
  claim one already held by another account
  (`internal/auth/import.go`, `emailAvailable`), and an import row that
  corrects an existing account's address retires that account's credential
  links. Tests: `TestUserImportRefusesDuplicateEmailAddresses`
  (`internal/auth/import_test.go`),
  `TestEmailAddressesStayUniqueAcrossAccounts` and
  `TestChangingAnEmailRetiresItsCredentialLinksAndQueuedMail`
  (`internal/auth/service_test.go`).
- Still open: concurrent session use after **disable** (behavior exists via
  the session lookup excluding disabled accounts, per `SECURITY.md`, but
  lacks a dedicated test), CSV import partial failures, and registration
  throttling. ROADMAP M1.

## D. Documented gaps the plans deliberately defer

These are in ROADMAP as future work with no committed mechanism. Do **not**
implement without an explicit request; when requested, they each need a
decision record first:

- Host-level network egress policy beyond Podman per-workspace networks
  (grep confirms zero firewall/nftables/iptables code — M9 future work).
- Metrics history and operational alerting (no metrics tables exist in
  migrations `0001`–`0023`; live sampling only — M6).
- Administrator-initiated named-volume restore/reattachment on behalf of a
  user; admin recovery remains download/remove only. **Resolved for the
  user-self-service case** (2026-08-14): a user can reattach their own
  retained volumes/directories via `/storage` and workspace creation —
  decision 0025, `internal/workspace/reattach_test.go`.
- File previews, bulk file operations, archive extraction (absent from
  `internal/files` and `internal/fileagent`; extraction requires a dedicated
  security design per `AGENTS.md` — M7).
- Packaging: systemd units, upgrade tooling (nothing under `deploy/` or
  `tools/` beyond reverse-proxy examples).
- Multi-instance anything (see B3/B4), PostgreSQL, HA, GPUs, host pools.

## E. Intentional design — do not "fix"

- Volume recovery restricted to download/remove: deliberate (`AGENTS.md`,
  decision 0022). Tombstones never authorize user access.
- Email never blocks lifecycle decisions (ROADMAP, decision 0015/0020). A
  mail failure must not prevent a stop or delete.
- No public workspace ports; terminal/desktop/file-manager are authenticated
  gateways with server-selected targets (`ARCHITECTURE.md` security
  boundaries). No generic reverse proxy, ever, without a new design.
- Explicit user quota overrides group quotas; missing quotas block ordinary
  users but not administrators; host capacity fails closed
  (`ARCHITECTURE.md` quota section, decision 0012/0013).
- Explicit deletion archives managed-directory data; **timeout cleanup never
  archives or deletes user data** (`AGENTS.md`; decision 0006/0016).
- Reconciliation is non-destructive (see B1) until a repair policy exists.

## Git/workflow state at audit time

- `main` was 6 commits ahead of `origin/main` (`4d3529a`..`e25fed0`,
  including terminal-cleanup hardening). For PR-based review tooling, push
  first; there was no open PR. There is no CI configuration in the
  repository — all verification is local.
- This document originally claimed the stray comment token `ponytail:` at
  `internal/workspace/template.go:63` was "removed alongside this document";
  that edit was never actually staged in `8c9d4ba`. It was committed
  separately six days later in `a6549c5` once the mismatch was noticed —
  a reminder that this file's claims need re-verifying against the working
  tree, not just trusted.

## Since the audit (updates, not a re-audit)

**2026-08-20.** A1 above is resolved (see its update). Separately, `main` was
found **red**: commit `2f270b3` rebased the idle-stop timeout from
`Workspace.StartedAt` onto `Workspace.IdleSince`
(`internal/workspace/timeouts.go`) without updating the
`internal/notifications` test fixtures, so both notification tests failed on a
clean checkout. The fixtures now set `IdleSince`. That trace also found a real
upgrade-path gap: migration `0026` added `idle_since` with `DEFAULT 0`, which
made every workspace running at upgrade time permanently exempt from the idle
stop. Migration `0027_backfill_idle_since.sql` seeds those rows from
`started_at`, covered by
`TestBackfillMigrationSeedsIdleSinceFromStartedAt`.

**2026-09-30.** A code-level security review of the auth/web/repository/file/
runtime layers (16 findings, recorded outside the repository) closed its
first item: a CSV import that corrected an existing account's email address
updated the row without retiring credential links already mailed to the old
address, so an outstanding invitation or reset link stayed redeemable from a
mailbox the account no longer owned. `invalidateCredentialTokensTx`
(`internal/repository/sqlite/store.go`) is now the shared body behind every
route that replaces a credential or changes an address — CSV import, the
administrator profile editor, `UpdateUserProfile`, and the new
`InvalidateUserCredentialTokens` — and it both deletes the account's
invitation and reset tokens and cancels queued `email_messages` of those
kinds. The same change added the one-address-one-account invariant (enforced
at creation, import, and the administrator editor) and an administrator
profile/password editor (`POST /admin/users/{id}/profile` and `/password`).
Setting a password there is refused for the administrator's own account
(`ErrSelfPasswordSet`, which has its own path proving knowledge of the current
password) and for a disabled account (`ErrTargetDisabled`, because re-opening
one is a separate audited decision); a password an administrator sets must be
changed at the target's next sign-in and revokes every session for that
account. `AGENTS.md`
and `SECURITY.md` carry the invariants. Still open from that review, in
priority order: the login limiter's check-then-act race
(`internal/auth/limiter.go`, `Allow` reads the counter without reserving, so a
parallel burst passes the five-failure cap), IP-only rate limiting behind a
reverse proxy (B4 here; needs a decision record), `CookieSecure` defaulting
false with no cross-check against an HTTPS external base URL, secrets accepted
as CLI flags, and the absent Content-Security-Policy header. Note also that
`users.email` has no unique index (migration `0004` adds it as plain
`TEXT NOT NULL DEFAULT ''`), so the uniqueness invariant currently rests on an
application-level check-then-act.

**2026-10-14.** Libpod decodes container-create requests without rejecting
unknown fields, and several COWS fields were silently ignored on the Libpod
path used for templates with a `container_user`. Each was confirmed against a
live Podman 6.1.2:

- Named volumes were sent in `mounts`, which Libpod treats as host bind
  sources, so every start failed. They now go in `volumes`. Fixed.
- `passwd_entry` does not exist before Podman 4.1.0, which then writes its
  fallback `1000:*:...:container user` line. The adapter now refuses older
  Podman (decision 0028). Fixed.
- The explicit UID/GID mapping was nested in `userns`, which only accepts
  `nsmode`/`value`. Containers ran in Podman's default rootless namespace,
  where container root is the COWS host account, contrary to decision 0009.
  The adapter now sends `userns.nsmode=private` with `idmappings`, and the
  file helper translates a configured identity through the same mapping.
  Fixed; the live test above fails on the old request.
- The isolated network was sent as `newNetworks`, which Libpod also ignores.
  A `container_user` workspace with `COWS_NETWORK_ISOLATION_ENABLED` joined
  the default `podman` network, with outbound access, instead of its internal
  network, so decision 0023 did not hold for those workspaces. The adapter now
  sends the untagged SpecGenerator key `Networks`; loopback desktop port
  publishing works on the internal network. Fixed; the compatibility path
  (no `container_user`) was already correct. Affected workspaces must be
  recreated.
- **Operational cost of the private user namespace:** rootless overlay
  storage cannot shift IDs at mount time, so the first container per image
  makes containers/storage build a re-owned copy of the whole image
  (`storage-chown-by-maps`). For the 14.6 GB `iic-osic-tools` image this took
  about twenty minutes, held Podman's image-store lock, and stalled every
  Podman API request, including COWS reconciliation and `/readyz`, until it
  finished. Because every workspace uses the same mapping, the copy is made
  once per image version: later creates took under 0.1 s, also after every
  container using the copy had been removed. **Open:** COWS's ten-second
  Podman client timeout fails the first create while Podman finishes it,
  leaving an unrecorded managed container behind. Candidate fix: build the
  copy during the explicit administrator image pull (decision 0019) and give
  container creation a longer timeout.
- **Open:** a self-service volume reattachment (decision 0025) whose
  workspace creation later fails loses the retained-volume tombstone. Seen
  when the timeout above rolled back a workspace reattaching
  `cows-WNVhCjcisobnjVsHVOf_sF16-designs` on the testing instance: the volume
  and its data remain, but neither self-service reattachment nor
  administrator recovery lists it any more. Directories have this guarantee
  (`TestDirectoryReattachmentSurvivesLaterFailure`); volumes need the same
  restore-on-failure path and test.
- **Open:** reconciliation records a `runtime.orphaned_container` audit event
  for the same orphan on every pass (every 30 s), about fifty identical
  events in 25 minutes. It should record each orphan once.

**2026-10-15.** Follow-up on the `storage-chown-by-maps` stall, measured
against a separate throwaway Podman 6.1.2 store and API service whose image
and layer locks were held, with a four-second limit per request:

- Why it stalls everything: containers/storage `CreateContainer` holds the
  layer-store and image-store write locks for the whole re-owned copy, and
  Podman's compatibility `/version` handler reads image counts from storage.
  The adapter calls `/version` before every request (`Adapter.get`,
  `Adapter.request`), so all COWS runtime calls wait.
- Still answering: `/_ping` and `/libpod/_ping`; `/_ping` reports the API
  version in its `Api-Version` and `Libpod-Api-Version` headers. Running
  containers and already-open desktop and terminal streams are unaffected.
- Waiting: `/version`, `/info`, `/libpod/info`, container list (both APIs),
  container inspect, stats, image inspect, exec create (new terminals), and
  container create, start, and stop.
- Not avoidable inside one store. Rootless overlay cannot shift IDs at
  mount time (containers/storage tries idmapped lower layers only as real
  root; metacopy is unavailable rootless), and the testing host's ext4
  storage cannot reflink, so the copy duplicates every byte.
- Ways to shorten or move it, none implemented: fuse-overlayfs as
  `mount_program` supports ID shifting for contiguous mappings and makes no
  copy, at a FUSE read cost for every container of the account (not
  benchmarked); Podman storage on XFS with reflink or btrfs should let
  overlay copy-up clone blocks instead of copying them (not measured); a
  copy prepared in a separate staging store and listed in
  `additionalimagestores` was reused by the live store, which created only
  the container layer with correct ownership, but the stall returned when
  the staging store was written while already listed, so each image version
  would need a fresh store, a Podman service restart to pick it up (storage
  configuration is read at service start; not tested), and cleanup of old
  stores. Kernel idmapped mounts need a rootful runtime and are ruled out.
- COWS-side mitigation, independent of the above: take the API version from
  `/_ping` or cache it instead of calling `/version` before every request,
  and report a busy runtime rather than an unavailable one. Workspace starts,
  stops, and new terminals would still wait for the copy.
- Recommended direction, pending a decision: build the copy during the
  explicit administrator image pull (amending decision 0019), give container
  creation a longer timeout, add the COWS-side mitigation, and recommend
  reflink-capable Podman storage; take the staging-store design only if a
  stall-free first create is required.

Findings A2 and C3 above were updated 2026-08-11 to reflect commits made
after the 2026-08-04 audit (`a6549c5`, `3ed253a`, `95ecb05`, `5876ec8`,
`b7b3056`). Two of those five are unrelated bug fixes with no open finding to
close: `95ecb05` stopped an out-of-range terminal resize from dropping the
whole session, and `5876ec8` fixed the CSRF cookie's `MaxAge` being hardcoded
to one hour while sessions default to eight, which broke every POST on a
long-lived session. `b7b3056` deduplicated the file-manager and file-agent ZIP
streaming into `internal/archive` (see `ARCHITECTURE.md`); no behavior
changed. Sections A1, A3, and B remain as audited — verify against the code
before relying on them, per the note above.
