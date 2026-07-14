# COWS

COWS (Containerized On-Demand Workspace System) is a web-based management
platform for containerized user workspaces. It is intended for universities,
research institutes, and similar teams that provide preconfigured software
environments through one authenticated HTTPS endpoint.

## Project status

COWS has completed the initial **Milestone 8: restricted file manager** checkpoint. The
current code provides local administrator bootstrap, password authentication,
server-side sessions, CSRF-protected forms, basic user management,
administrator-managed workspace templates, Docker inspection and lifecycle
operations, workspace persistence, quotas, fail-closed capacity checks, and
template-controlled timeout processing, an authenticated browser terminal, and
an authenticated noVNC desktop gateway through the Docker-compatible runtime
adapter, and a restricted file manager for approved directory mounts. It does
not provide generic proxied application access, archive operations, or
named-volume file access yet. It is not production-ready.

## Goals

- Give users a central, authenticated way to manage and access their workspaces.
- Keep container runtime access behind a small internal adapter boundary.
- Enforce ownership, quotas, and approved templates in the backend.
- Reconcile database state with the actual Docker or Podman runtime.
- Keep installation small: one Go service, SQLite, and locally served assets.

Generic proxied applications and expanded file management remain planned work. See
[PROJECT.md](PROJECT.md),
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
Docker inspection and lifecycle operations use `/var/run/docker.sock` by
default and can be changed with `COWS_DOCKER_SOCKET` or `-docker-socket`.
COWS creates and manages only COWS-labeled containers using approved template
images, server-side resource limits, and isolated networking. It does not
expose container ports publicly. Terminal sessions use an approved `/bin/sh -l`
and desktop sessions use a template-approved internal VNC service through
authenticated COWS WebSockets; neither is a generic user-selected proxy.
Desktop-enabled workspaces can use an administrator-defined static or generated
VNC password through secret placeholders; COWS supplies it automatically to
noVNC after authorization, so users do not enter a second password.
Rootless Podman can be used through its Docker-compatible API socket, for
example `/run/user/1000/podman/podman.sock`; COWS checks runtime-reported
capabilities and refuses workspace creation when required limits cannot be
enforced.
Workspace capacity checks also require `COWS_HOST_STORAGE_BYTES` to be set to
the initial storage amount COWS may allocate. The value seeds the persistent
host settings row only when it does not exist; administrators can change host
storage and reserved CPU, memory, and storage in **Settings** without
restarting COWS. Zero leaves storage capacity unknown and causes workspace
creation to fail closed. Directory mounts are created below `COWS_MOUNT_ROOT`
(default `./data/cows-mounts`) using engine-managed workspace names. Only
directory mounts marked for file-manager access are visible in the browser;
named volumes remain runtime-managed.

## Security warning

COWS is an early development project. Do not expose it to untrusted users or
the public internet until the applicable authentication, authorization,
runtime-isolation, HTTPS, audit, and operational controls are implemented and
reviewed. See [SECURITY.md](SECURITY.md).

## License

The project license has not yet been selected. Do not assume that the current
repository is ready for redistribution under a particular license.
