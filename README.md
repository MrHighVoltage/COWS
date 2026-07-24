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

COWS is an early, active-development implementation. Milestone 0 is complete;
the initial implementations of local accounts, templates, workspace lifecycle,
terminal access, desktop access, email warnings, and the restricted file
manager are present. The current runtime is **rootless Podman only**. Docker is
intentionally out of scope.

Implemented today:

- local administrator bootstrap, login/logout, sessions, password changes,
  first-login password changes, optional self-registration, CSV user import,
  groups, quotas, disable/delete safety, and basic audit persistence;
- administrator templates with typed runtime configuration, server-resolved
  environment placeholders, managed directory and volume mounts, resource
  selection, terminal UID allowlists, VNC secrets, image availability checks,
  explicit image pulls, and template copying;
- workspace create/start/stop/restart/delete, desired and observed state,
  reconciliation, timeout stop/delete processing, measured storage, quota and
  host-capacity admission, and explicit managed-directory archival;
- authenticated xterm.js terminal sessions, noVNC desktop sessions, and a
  restricted file manager with bounded uploads, mutations, downloads, and
  streamed directory ZIP downloads;
- local HTMX, Alpine.js, xterm.js, noVNC, and Nerd Font assets embedded in the
  binary, plus optional lifecycle warning email delivery.

The implementation is not production-ready. Remaining high-impact gaps include
robust recovery of partial lifecycle operations, a constrained web-application
proxy, historical metrics, volume restore, and deployment hardening. Local
password reset, richer audit/metrics views, named-volume recovery, and optional
network isolation are implemented. See [ROADMAP.md](ROADMAP.md) for the
complete status.

## Goals

- Give users a central, authenticated way to manage and access their workspaces.
- Keep container runtime access behind a small internal adapter boundary.
- Enforce ownership, quotas, and approved templates in the backend.
- Reconcile database state with the actual rootless Podman runtime.
- Keep installation small: one Go service, SQLite, and locally served assets.

Generic proxied applications and broader file operations remain planned work. See
[PROJECT.md](PROJECT.md),
[ARCHITECTURE.md](ARCHITECTURE.md), and [ROADMAP.md](ROADMAP.md) for scope and
design decisions.

## Initial deployment

The first deployment target is one Linux server with one active COWS process,
one SQLite database, and one local rootless Podman runtime. A reverse proxy may
terminate HTTPS. COWS must not expose workspace VNC, SSH, terminal, or
application ports directly to users. See [docs/configuration.md](docs/configuration.md)
and [docs/deployment.md](docs/deployment.md) for commands, configuration, and
prepared Caddy, nginx, and Apache proxy examples.

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
`./data/cows.db`. See [docs/configuration.md](docs/configuration.md) for all
environment variables, flags, rootless Podman setup, bootstrap credentials,
and production operation.

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
