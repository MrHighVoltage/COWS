# COWS Agent Instructions

## Identity and scope

COWS means **Containerized On-Demand Workspace System**. Always write the
project and repository name as uppercase `COWS`. Read `PROJECT.md`,
`ARCHITECTURE.md`, `SECURITY.md`, `ROADMAP.md`, and relevant decision records
before changing behavior. Workspace lifecycle and timeout work must follow
`docs/decisions/0004-workspace-state-model.md` and
`docs/decisions/0006-workspace-timeouts.md`. Template runtime configuration
must follow `docs/decisions/0007-template-runtime-configuration.md`.

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
Docker argument maps, unrestricted placeholders, arbitrary host paths, public
port bindings, or browser-controlled environment values.

Terminal access is implemented through the runtime shell capability and must
retain fixed server-selected commands, ownership/template/state checks,
WebSocket session limits, resize validation, and audit events. Do not expose
public workspace ports. Desktop access is implemented only through the fixed
template-approved VNC service and must retain loopback mapping verification,
WebSocket session limits, audit events, and template-selected VNC credentials
without exposing them in URLs or logs. Do not implement a generic proxy,
file-manager, or multi-host runtime feature prematurely.

Workspace timeout policies are backend-enforced and must not depend on browser
timers. Keep the initial no-connection stop, stopped-container deletion, and
post-deletion data-archive eligibility timestamps distinct. Do not implement
email delivery or archive actions until their dedicated milestones; preserve
clear future notification hooks and audit semantics.

## Development commands

```sh
gofmt -w .
go test ./...
go vet ./...
go build -o bin/cows ./cmd/cows
```

If an optional analyzer is installed, run it too. Docker or Podman must not be
required for the ordinary unit-test suite.

## Working practices

Keep changes small and reviewable. Use table-driven tests and `httptest` where
appropriate. Explain non-obvious behavior with short comments that describe
why it exists. Update architecture/security documentation when boundaries or
data flow change. Keep future work in `ROADMAP.md` rather than adding
speculative packages. Add tests for timeout boundaries, restart/reconcile
behavior, authorization, terminal isolation, and irreversible operations.
Preserve unrelated user changes in a dirty worktree.
