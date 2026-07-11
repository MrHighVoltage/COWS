# COWS Agent Instructions

## Identity and scope

COWS means **Containerized On-Demand Workspace System**. Always write the
project and repository name as uppercase `COWS`. Read `PROJECT.md`,
`ARCHITECTURE.md`, `SECURITY.md`, and `ROADMAP.md` before changing behavior.

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

Do not implement future terminal, desktop, proxy, file-manager, or runtime
features prematurely. Do not expose public workspace ports.

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
speculative packages. Preserve unrelated user changes in a dirty worktree.
