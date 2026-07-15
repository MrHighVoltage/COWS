# COWS Agent Instructions

## Identity and scope

COWS means **Containerized On-Demand Workspace System**. Always write the
project and repository name as uppercase `COWS`. Read `PROJECT.md`,
`ARCHITECTURE.md`, `SECURITY.md`, `ROADMAP.md`, and relevant decision records
before changing behavior. Workspace lifecycle and timeout work must follow
`docs/decisions/0004-workspace-state-model.md` and
`docs/decisions/0006-workspace-timeouts.md`. Template runtime configuration
must follow `docs/decisions/0007-template-runtime-configuration.md`.
Resource, storage, rootless-Podman, and group-access changes must follow
`docs/decisions/0012-rootless-podman-resource-and-group-policy.md`.
Group quota and administrator-view changes must follow
`docs/decisions/0013-group-quotas-and-administrator-views.md`.
Registration and account-credential changes must follow
`docs/decisions/0014-self-registration-and-account-credentials.md`.
Email notification changes must follow
`docs/decisions/0015-email-notifications.md`.

## Technology constraints

- Use conventional Go and server-rendered `html/template` pages.
- Use SQLite initially with explicit SQL and application-controlled migrations.
- Use HTMX for server interactions and Alpine.js only for small local UI state.
- Serve browser dependencies locally at pinned versions; do not use public CDNs
  in production.
- Do not introduce Node.js, npm, TypeScript, React, Vue, Svelte, Angular,
  bundlers, Kubernetes, microservices, or PostgreSQL for the initial system.
- Prefer the Go standard library. Add focused dependencies only when they
  remove specialized complexity and document their versions and licenses.

## Security rules

Treat the browser as untrusted. Every backend operation independently checks
authentication, authorization, ownership, policy, and workspace state. Never
accept a browser-selected runtime ID, backend target, internal URL, arbitrary
port, image, mount, capability, device, or runtime argument. Keep the runtime
socket behind the runtime interface, fail closed on missing authorization or
capacity data, and do not log secrets or user content.

Template runtime configuration is typed and server-resolved. Do not add raw
runtime argument maps, unrestricted placeholders, arbitrary host paths, public
port bindings, or browser-controlled environment values.

Terminal access is implemented through the runtime shell capability and must
retain fixed server-selected commands, ownership/template/state checks,
WebSocket session limits, resize validation, and audit events. Do not expose
public workspace ports. Desktop access is implemented only through the fixed
template-approved VNC service and must retain loopback mapping verification,
WebSocket session limits, audit events, and template-selected VNC credentials
without exposing them in URLs or logs. Do not implement a generic proxy or
multi-host runtime feature prematurely. The initial file manager is limited to
authorized directory or named-volume mounts explicitly marked `file_manager`;
use the runtime file-access capability and rooted, server-selected paths for
all file operations. Keep runtime storage paths and volume names out of
browser routes.

Rootless Podman directory mounts with a configured container user use explicit
subordinate UID/GID mappings, not `keep-id`, and keep the COWS-owned
per-container parent separate from mapped inner mount directories. Explicit
workspace deletion archives the complete per-container directory in the
sibling archive root; timeout cleanup must not silently delete or archive user
data. Explicit deletion retains named volumes and must persist their tombstone
metadata before removing the workspace record. Retained-volume metadata does
not authorize restore, mount, or cleanup. Directory ZIP downloads are streamed and bounded;
do not add archive extraction without dedicated security tests.

Workspace timeout policies are backend-enforced and must not depend on browser
timers. Keep the initial no-connection stop and stopped-container deletion
timestamps. Automatic timeout cleanup must never delete or archive user data;
explicit deletion's managed-directory archive is separate. Do not reintroduce
the removed post-deletion data-retention configuration. Reset the recorded
connection timestamp whenever a workspace enters a new running period.

Never render raw runtime, filesystem, or database errors to ordinary users.
Persist stable public error categories separately from administrator-side
diagnostic detail.

Self-registration is disabled by default. When enabled, it creates only user
accounts, requires a valid email address and password confirmation, applies
server-configured default quota and group membership atomically, and must not
accept a role, quota, group, or template from the browser. Do not add email
verification or password-reset tokens without a dedicated security design.

Email delivery is optional and must not block workspace lifecycle operations.
Use a persisted, deduplicated notification boundary with retries, never log
SMTP credentials or message contents, and keep warning delivery separate from
the authoritative timeout worker.

## Development commands

```sh
gofmt -w .
go test ./...
go vet ./...
go build -o bin/cows ./cmd/cows
```

If an optional analyzer is installed, run it too. Podman must not be
required for the ordinary unit-test suite.

## Working practices

Keep changes small and reviewable. Use table-driven tests and `httptest` where
appropriate. Explain non-obvious behavior with short comments that describe
why it exists. Update architecture/security documentation when boundaries or
data flow change. Keep future work in `ROADMAP.md` rather than adding
speculative packages. Add tests for timeout boundaries, restart/reconcile
behavior, authorization, terminal isolation, and irreversible operations.
Preserve unrelated user changes in a dirty worktree.
