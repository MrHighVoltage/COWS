# COWS

COWS (Containerized On-Demand Workspace System) is a web-based management
platform for containerized user workspaces. It is intended for universities,
research institutes, and similar teams that provide preconfigured software
environments through one authenticated HTTPS endpoint.

> **Active development:** COWS is under active development and is not
> production-ready.
>
> **Development note:** This project is currently 100% vibe-coded. Review,
> testing, and security validation are required before production use.

## Project status

COWS has completed the initial **Milestone 8: restricted file manager** checkpoint. The
current code provides local administrator bootstrap, optional self-registration,
password authentication,
server-side sessions, CSRF-protected forms, basic user management,
administrator-managed workspace templates, rootless Podman inspection and lifecycle
operations, workspace persistence, quotas, fail-closed capacity checks, and
template-controlled timeout processing, an authenticated browser terminal, and
an authenticated noVNC desktop gateway through the Podman runtime
adapter, a restricted file manager for approved directory and named-volume
mounts, and optional lifecycle email warnings. Rootless Podman file access uses
the local namespace helper described
in [ADR 0011](docs/decisions/0011-runtime-file-access.md). It does not provide
generic proxied application access, archive extraction, or automatic retention
archival. It is not production-ready.

## Goals

- Give users a central, authenticated way to manage and access their workspaces.
- Keep container runtime access behind a small internal adapter boundary.
- Enforce ownership, quotas, and approved templates in the backend.
  - Reconcile database state with the actual rootless Podman runtime.
- Keep installation small: one Go service, SQLite, and locally served assets.

Generic proxied applications and expanded file management remain planned work. See
[PROJECT.md](PROJECT.md),
[ARCHITECTURE.md](ARCHITECTURE.md), and [ROADMAP.md](ROADMAP.md) for scope and
design decisions.

## Initial deployment

The first deployment target is one Linux server with one active COWS process,
one SQLite database, and one local rootless Podman runtime. A reverse proxy may
terminate HTTPS. COWS must not expose workspace VNC, SSH, terminal, or
application ports directly to users. See [docs/deployment.md](docs/deployment.md)
and the example Caddy configuration for the prepared, non-development proxy
deployment.

## Development

Requirements: Go 1.26 or newer within the supported Go release policy. Node.js
and npm are deliberately not required.

```sh
tools/web-assets.sh verify
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
Self-registration is disabled by default. To enable it, set
`COWS_REGISTRATION_ENABLED=true`; configure
`COWS_REGISTRATION_DEFAULT_GROUPS` with comma-separated existing group names
and adjust the `COWS_REGISTRATION_DEFAULT_*` quota variables as needed. The
defaults are finite and are applied server-side; registrants cannot choose
their role, groups, or quota.
Email warnings are disabled by default. To enable them, set
`COWS_EMAIL_ENABLED=true`, configure `COWS_SMTP_HOST`, `COWS_SMTP_PORT`, and
`COWS_SMTP_FROM`, and provide SMTP credentials when required. STARTTLS is
required by default and can only be relaxed explicitly for a trusted local
relay. Warning lead time and retry interval use
`COWS_EMAIL_WARNING_LEAD_TIME` and `COWS_EMAIL_RETRY_INTERVAL`.
COWS uses the rootless Podman service socket at
`/run/user/<uid>/podman/podman.sock` by default. Set `COWS_PODMAN_SOCKET` or
`-podman-socket` to select another rootless Podman socket. Rootful Podman and
Docker sockets are rejected.
COWS creates and manages only COWS-labeled containers using approved template
images, server-side resource limits, and isolated networking. It does not
expose container ports publicly. Terminal sessions use the template's
server-resolved container-user shell with `-l` (or `/bin/sh -l` when no
container user is configured), and desktop sessions use a template-approved
internal VNC service through
authenticated COWS WebSockets; neither is a generic user-selected proxy.
Desktop-enabled workspaces can use an administrator-defined static or generated
VNC password through secret placeholders; COWS supplies it automatically to
noVNC after authorization, so users do not enter a second password.
COWS checks rootless Podman-reported capabilities and refuses workspace
creation when required limits cannot be enforced. The adapter may use Podman's
compatibility API endpoints internally, but Docker is not a supported runtime.
Workspace capacity checks also require `COWS_HOST_STORAGE_BYTES` to be set to
the initial storage amount COWS may allocate. The value seeds the persistent
host settings row only when it does not exist; administrators can change host
storage and reserved CPU, memory, and storage in **Settings** without
restarting COWS. Zero leaves storage capacity unknown and causes workspace
creation to fail closed. Directory mounts are created below `COWS_MOUNT_ROOT`
(default `./data/cows-mounts`) in per-container directories named from the
immutable COWS workspace identifier. Explicit deletion moves that complete
directory to `COWS_MOUNT_ARCHIVE_ROOT` (default `./data/cows-mounts-archive`)
on the same filesystem. Directory and named-volume mounts marked for
file-manager access are visible in the browser. File operations are resolved
by COWS and the runtime adapter; runtime storage paths and volume names are
never browser inputs.

## Security warning

COWS is an early development project. Do not expose it to untrusted users or
the public internet until the applicable authentication, authorization,
runtime-isolation, HTTPS, audit, and operational controls are implemented and
reviewed. See [SECURITY.md](SECURITY.md).

## License

COWS is licensed under the **GNU Affero General Public License, version 3 or
any later version**. See [LICENSE](LICENSE).

Commercial use, hosted services, and selling services built with COWS are
allowed. The AGPL copyleft and network-source requirements apply to modified
versions that are conveyed or offered for remote network interaction. COWS
project contributions are intended to remain under the AGPL.
