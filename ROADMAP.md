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

## Milestone 1: Local users, registration, and authorization foundation (in progress)

Implemented so far: administrator bootstrap, bcrypt password hashing,
login/logout, mandatory first-login password changes, authenticated password
changes, stored user email addresses, opaque server-side sessions, CSRF
protection, user and administrator roles, basic user management, and the first
audit events. Disabled-by-default self-registration now requires email and
password confirmation, applies server-assigned default quotas and groups
atomically, and has registration rate limiting. Remaining exit work includes
authorization coverage for every state-changing handler, operational audit
review, a documented recovery procedure, and broader registration-abuse tests.
Login and registration rate limiting are process-local and must move to shared
infrastructure if COWS ever runs multiple active instances.

## Milestone 2: Templates and runtime inspection (in progress)

Validated administrator templates, role access rules, typed command/environment/
mount/service configuration, workspace configuration snapshots, persisted port
allocations, resource policy fields, the COWS-facing runtime interface, an
inspection coordinator, and a rootless Podman adapter are now present. Remaining work is stronger runtime connectivity
reporting, orphan/partial-operation reconciliation, fake-runtime contract
coverage, and runtime-enforced storage policy. The adapter detects rootless
Podman capabilities and refuses unsafe creates when required CPU, memory, or
process limits are unavailable. Docker support is intentionally out of scope.
A dedicated secret
store, graphical gateway routing, and separate reusable administrator port-pool
management remain future work.

## Milestone 3: Workspace lifecycle (persistence prepared)

Workspace persistence, owner/template foreign keys, desired/observed state,
user creation/listing, quota assignment, and deterministic quota/host-capacity
admission checks are now present, including persistent administrator-managed
host capacity and reserved-resource settings. Administrator-defined initial
connection and stopped-container retention timeouts, user-visible policy
details, rootless Podman lifecycle operations, and a
reconciler-driven timeout worker are now present. Warning-event hooks exist in
the lifecycle model. Each start resets the initial-connection observation period, and
explicit deletion records retained named-volume metadata before removing the
workspace record. Remaining
exit work includes reconciliation handling for orphaned and partially-created
objects, runtime-enforced storage policy, idempotence across restart,
long-running operation execution, and irreversible-operation failure-path
tests. The user page now shows allocated resources, quota progress, operation
status, and automatically refreshed state.

## Milestone 4: Terminal access

Completed initial implementation: local xterm.js 5.3.0 assets, an authenticated
WebSocket, approved `/bin/sh -l` execution, resize handling, idle and maximum
session expiry, cleanup, audit events, and Podman exec stream adapter tests.
Exit criteria for this checkpoint are met. Browser accessibility review and
rootless Podman integration tests remain hardening work.

## Milestone 5: Graphical desktop access

Completed initial implementation: local noVNC 1.6.0 core modules, an
authenticated COWS WebSocket, template-controlled `desktop` TCP service access,
loopback port verification, session cleanup, and tests proving that non-loopback
VNC mappings are rejected. Templates can define static or generated secrets,
bind one to the desktop service, and use it through `{{cows.secret.name}}` in
environment values; COWS supplies the selected value automatically to noVNC
after authorization. Browser accessibility review and rootless Podman
integration tests against VNC-enabled images remain hardening work.

## Milestone 6: Workspace web applications

Add template-defined applications and a constrained authenticated proxy with
WebSocket support, SSRF defenses, origin/redirect handling, and size/time
limits. Do not create a generic reverse proxy.

## Milestone 7: Resource policies and email notifications

The initial optional email implementation now warns about upcoming automatic
stop and deletion using the standard library SMTP client, persisted
deduplication, bounded retries, and a separate worker. Remaining work includes
live resource display, richer host capacity views, idle shutdown, expiration,
cleanup policies, and administrator capacity inspection. Keep high-frequency
samples out of the main SQLite control-plane tables. Email must never block or
decide lifecycle operations.

Timeout policy execution belongs to Milestone 3. Milestone 7 may add richer
idle detection and resource-driven policies, but must not replace the explicit
timeout phases with browser-only or metrics-only behavior.

Email warning exit criteria:

- SMTP is disabled unless explicitly configured.
- Upcoming stop and deletion warnings are deduplicated per workspace deadline.
- Delivery retries are persisted and bounded without blocking reconciliation.
- Messages contain only the workspace name, action, and deadline.
- SMTP credentials and message contents are absent from logs and audit events.

## Milestone 8: Restricted file manager

Templates can enable the `files` access method and mark approved directory or
named-volume mounts read-only or read-write. COWS creates engine-managed
directory names below its configured mount root, supports server-side listings,
bounded uploads, downloads, folder creation, rename, deletion, and streamed
bounded ZIP downloads. Rootless Podman operations use the runtime-backed file
access helper in ADR 0011, preserving the mapped container identity. Explicit
workspace deletion archives managed directory mounts while timeout cleanup
leaves data in place. Explicit deletion retains named volumes with durable
control-plane tombstones for later administrator recovery or cleanup. Remaining
exit work is stronger temporary storage and
file-count policy, symlink-race
hardening, audit events for every file mutation, and broader browser
accessibility review.

## Later

Evaluate a privileged multi-host COWS agent, host pools, PostgreSQL, high
availability, external metrics, GPUs, shared storage, and advanced policy only
when deployment requirements justify them.
