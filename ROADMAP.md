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
login/logout, opaque server-side sessions, CSRF protection, user and
administrator roles, basic user management, and the first audit events. The
remaining exit work includes authorization coverage for every state-changing
handler, operational audit review, and a documented recovery procedure. Login
failure rate limiting is now process-local and must move to shared infrastructure
if COWS ever runs multiple active instances.

## Milestone 2: Templates and runtime inspection (interface prepared)

The COWS-facing runtime interface and managed-label convention are prepared,
but no adapter or runtime operation exists yet. Add validated administrator
templates, template access rules, one read-only runtime adapter,
health/capability reporting, managed-container inspection, and fake-runtime
contract tests. Preserve a practical second adapter for Docker or Podman.

## Milestone 3: Workspace lifecycle

Add create/start/stop/restart/delete, ownership checks, runtime-enforced limits,
quota and host-capacity checks, desired/observed state reconciliation, and
administrator overrides. Exit requires idempotence and failure-path tests.

## Milestone 4: Terminal access

Integrate xterm.js with an authenticated WebSocket, approved shell execution,
resize handling, session expiry, cleanup, and audit events. No arbitrary command
or runtime target may come from the browser.

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

## Milestone 8: Restricted file manager

Add approved roots, safe listings, uploads/downloads, generated ZIPs, and only
then safe extraction if justified. Add path, symlink, archive, size, count, and
temporary-storage tests before enabling it.

## Milestone 9: Institutional authentication

Add OpenID Connect, account linking/provisioning policy, role mapping, and a
recovery-administrator strategy. Local recovery access must remain deliberate
and secure.

## Later

Evaluate a privileged multi-host COWS agent, host pools, PostgreSQL, high
availability, external metrics, GPUs, shared storage, and advanced policy only
when deployment requirements justify them.
