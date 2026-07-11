# COWS

COWS (Containerized On-Demand Workspace System) is a web-based management
platform for containerized user workspaces. It is intended for universities,
research institutes, and similar teams that provide preconfigured software
environments through one authenticated HTTPS endpoint.

## Project status

COWS is at **Milestone 2 in progress: templates and runtime inspection**. The
current code provides local administrator bootstrap, password authentication,
server-side sessions, CSRF-protected forms, basic user management, and
administrator-managed workspace templates. It does not create containers or
provide workspace access. It is not production-ready.

## Goals

- Give users a central, authenticated way to manage and access their workspaces.
- Keep container runtime access behind a small internal adapter boundary.
- Enforce ownership, quotas, and approved templates in the backend.
- Reconcile database state with the actual Docker or Podman runtime.
- Keep installation small: one Go service, SQLite, and locally served assets.

Workspace terminal access, graphical desktop access, proxied applications, and
file management are planned milestones. See [PROJECT.md](PROJECT.md),
[ARCHITECTURE.md](ARCHITECTURE.md), and [ROADMAP.md](ROADMAP.md) for scope and
design decisions.

## Initial deployment

The first deployment target is one Linux server with one active COWS process,
one SQLite database, and a local Docker or Podman runtime. A reverse proxy may
terminate HTTPS. COWS must not expose workspace VNC, SSH, terminal, or
application ports directly to users.

## Development

Requirements: Go 1.26 or newer within the supported Go release policy. Node.js
and npm are deliberately not required.

```sh
go test ./...
go vet ./...
go build -o bin/cows ./cmd/cows
./bin/cows
```

The server defaults to `127.0.0.1:8080` and creates its SQLite database at
`./data/cows.db`. Configuration may also be supplied with flags or the
`COWS_LISTEN_ADDR`, `COWS_DATABASE_PATH`, `COWS_LOG_LEVEL`, and
`COWS_SHUTDOWN_TIMEOUT` environment variables. For a new local database, set
`COWS_BOOTSTRAP_ADMIN_USERNAME` and `COWS_BOOTSTRAP_ADMIN_PASSWORD` together;
the bootstrap is attempted only when no users exist. Use
`COWS_COOKIE_SECURE=true` when serving through HTTPS.
Docker inspection uses `/var/run/docker.sock` by default and can be changed
with `COWS_DOCKER_SOCKET` or `-docker-socket`. The current Docker integration is
read-only; it does not create or modify containers.

## Security warning

COWS is an early development project. Do not expose it to untrusted users or
the public internet until the applicable authentication, authorization,
runtime-isolation, HTTPS, audit, and operational controls are implemented and
reviewed. See [SECURITY.md](SECURITY.md).

## License

The project license has not yet been selected. Do not assume that the current
repository is ready for redistribution under a particular license.
