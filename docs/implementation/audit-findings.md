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
  (`docs/decisions/NNNN-*.md`, next number: 0024) when behavior or policy
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

### A1. No administrator credential-recovery mechanism or procedure

- **ROADMAP M1 exit criterion** ("define recovery procedures for the first
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

### A2. No SQLite backup/restore story

- The entire control plane is one SQLite file plus the mount/archive roots,
  but no backup or restore procedure exists anywhere. It appears only in the
  ROADMAP "Later" list; for a first real deployment this is the largest
  documented-risk gap. Nothing in `internal/`, `tools/`, or `deploy/`
  references backup (no `VACUUM INTO`, `.backup`, or script).
- Guidance: this may be decided to be documentation-only at first — a
  `docs/deployment.md` section covering a safe online backup of
  `data/cows.db` (e.g. `sqlite3 cows.db '.backup ...'` against WAL mode), the
  mount and archive roots, and restore verification. Decide with the human
  before adding a tooling script. See `internal/database/` for the connection
  settings (WAL, busy timeout) the procedure must respect.

### A3. Readiness endpoint does not check the runtime

- `ARCHITECTURE.md` "Operational direction" states readiness should include
  database **and runtime connectivity**, but this requirement never made it
  into ROADMAP milestone criteria and is not implemented.
- Evidence: `internal/web/server.go:3317-3328` (`health`) and
  `internal/web/server.go:3331-3339` (`snapshot`) only `PingContext` the
  database; Podman is never probed. Routes at
  `internal/web/server.go:392-393`.
- Guidance: extend the health snapshot with a runtime connectivity check
  through the runtime interface (the adapter already exposes a version/host
  info call — do not leak Podman types into `internal/web`). Keep the check
  cheap and bounded in time; keep returning 200 for liveness-style probes if
  that distinction matters, or add a separate readiness route — record the
  choice. Match the existing `healthSnapshot` pattern and the admin health
  fragment at `internal/web/server.go:3310`.

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

### C2. Symlink-race coverage is unit-level only

- Unit tests exist (`internal/files/service_test.go`,
  `internal/fileagent/helper_test.go`), but ROADMAP M7 asks for stronger
  integration coverage of the file helper's rooted path handling under
  rootless namespace conditions.
- Guidance: extend C1's tagged suite rather than simulating races in unit
  tests. Do not weaken the rooted-path or per-workspace file-lock design
  (`internal/workspace/file_access.go`) while adding tests.

### C3. Abuse, session-invalidation, and import-failure tests

- ROADMAP M1. Session invalidation on password change/disable exists as
  behavior (`ARCHITECTURE.md` accounts section); verify and extend coverage
  in `internal/auth` and `internal/web/server_test.go` for: concurrent
  session use after disable, CSV import partial failures and duplicate
  handling, and registration throttling.

## D. Documented gaps the plans deliberately defer

These are in ROADMAP as future work with no committed mechanism. Do **not**
implement without an explicit request; when requested, they each need a
decision record first:

- Host-level network egress policy beyond Podman per-workspace networks
  (grep confirms zero firewall/nftables/iptables code — M9 future work).
- Metrics history and operational alerting (no metrics tables exist in
  migrations `0001`–`0023`; live sampling only — M6).
- Named-volume restore/reattachment (admin recovery is download/remove only,
  `internal/web/server.go:2459,2495` — M8/later).
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
- Fixed alongside this document: the stray comment token `ponytail:` at
  `internal/workspace/template.go:63` was removed (comment-only change).
