# COWS Roadmap

This roadmap is a sequence of reviewable milestones, not a promise that every
feature will be implemented. Each milestone must meet its exit criteria before
the next one expands the security surface.

## Milestone 0: Project foundation

Scope: documentation, Go module, configuration, structured logging, graceful
HTTP server, SQLite migration mechanism, templates, local HTMX/Alpine assets,
health page, one HTMX interaction, and basic tests.

Exit criteria:

- `go test ./...`, `go vet ./...`, and `go build ./cmd/cows` pass.
- The binary starts with documented defaults and shuts down on SIGINT/SIGTERM.
- `/healthz` returns a stable JSON health response.
- A browser page is rendered by Go templates and served with local assets.
- A button receives a server-rendered health fragment through HTMX.
- SQLite initializes with WAL, foreign keys, busy timeout, and one migration.
- No Node.js, npm, frontend build step, runtime socket, or container operation
  is required.
- Documentation describes the architecture, security limits, and next steps.

## Milestone 1: Local users and authorization foundation (in progress)

Implemented so far: administrator bootstrap, bcrypt password hashing,
login/logout, mandatory first-login password changes, stored user email
addresses, opaque server-side sessions, CSRF protection, user and administrator
roles, basic user management, and the first audit events. The remaining exit
work includes authorization coverage for every state-changing handler,
operational audit review, and a documented recovery procedure. Login failure
rate limiting is now process-local and must move to shared infrastructure if
COWS ever runs multiple active instances.

## Milestone 2: Templates and runtime inspection (in progress)

Validated administrator templates, role access rules, typed command/environment/
mount/service configuration, workspace configuration snapshots, persisted port
allocations, resource policy fields, the COWS-facing runtime interface, an
inspection coordinator, and a Docker Engine adapter are now present. Remaining work is stronger runtime connectivity
reporting, orphan/partial-operation reconciliation, fake-runtime contract
coverage, and runtime-enforced storage policy. The Docker-compatible adapter
now detects rootless Podman capabilities and refuses unsafe creates when
required CPU, memory, or process limits are unavailable. A practical native
second adapter for Podman remains deliberately deferred. A dedicated secret
store, graphical gateway routing, and separate reusable administrator port-pool
management remain future work.

## Milestone 3: Workspace lifecycle (persistence prepared)

Workspace persistence, owner/template foreign keys, desired/observed state,
user creation/listing, quota assignment, and deterministic quota/host-capacity
admission checks are now present, including persistent administrator-managed
host capacity and reserved-resource settings. Administrator-defined initial
connection, stopped-container retention, and post-deletion data-retention
timeouts, user-visible policy details, Docker lifecycle operations, and a
reconciler-driven timeout worker are now present. Warning-event hooks exist in
the lifecycle model, but email and archive actions remain disabled. Remaining
exit work includes reconciliation handling for orphaned and partially-created
objects, runtime-enforced storage policy, idempotence across restart,
long-running operation execution, and irreversible-operation failure-path
tests. The user page now shows allocated resources, quota progress, operation
status, and automatically refreshed state.

## Milestone 4: Terminal access

Completed initial implementation: local xterm.js 5.3.0 assets, an authenticated
WebSocket, approved `/bin/sh -l` execution, resize handling, idle and maximum
session expiry, cleanup, audit events, and Docker exec stream adapter tests.
Exit criteria for this checkpoint are met. Browser accessibility review and
runtime integration tests against supported Docker and Podman configurations
remain hardening work.

## Milestone 5: Graphical desktop access

Integrate noVNC through an authenticated COWS WebSocket, template-controlled
desktop access, session cleanup, and tests proving that VNC ports are private.

## Milestone 6: Workspace web applications

Add template-defined applications and a constrained authenticated proxy with
WebSocket support, SSRF defenses, origin/redirect handling, and size/time
limits. Do not create a generic reverse proxy.

## Milestone 7: Resource policies

Add live resource display, host capacity views, idle shutdown, expiration,
cleanup policies, and administrator capacity inspection. Keep high-frequency
samples out of the main SQLite control-plane tables.

Timeout policy execution belongs to Milestone 3. Milestone 7 may add richer
idle detection and resource-driven policies, but must not replace the explicit
timeout phases with browser-only or metrics-only behavior.

## Milestone 8: Restricted file manager

Add approved roots, safe listings, uploads/downloads, generated ZIPs, and only
then safe extraction if justified. Add path, symlink, archive, size, count, and
temporary-storage tests before enabling it.

## Later

Evaluate a privileged multi-host COWS agent, host pools, PostgreSQL, high
availability, external metrics, GPUs, shared storage, and advanced policy only
when deployment requirements justify them.
