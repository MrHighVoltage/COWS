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
  groups, quotas, disable/delete safety, local password reset by email, and
  audit persistence;
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
  binary, plus optional lifecycle warning and password-reset email delivery;
- administrator audit and live metrics views, retained named-volume recovery,
  and optional per-workspace internal Podman network isolation.

The implementation is not production-ready. Remaining high-impact gaps include
administrator credential recovery, documented SQLite and data-root backup and
restore, runtime-aware readiness reporting, restart-safe recovery of partial
lifecycle operations, rootless-Podman integration coverage, historical metrics,
volume restore, and deployment hardening. Generic web-application proxying and
other public service exposure are intentionally out of scope. See
[ROADMAP.md](ROADMAP.md) for the complete status and
[docs/implementation/audit-findings.md](docs/implementation/audit-findings.md)
for the latest implementation audit.

## Goals

- Give users a central, authenticated way to manage and access their workspaces.
- Keep container runtime access behind a small internal adapter boundary.
- Enforce ownership, quotas, and approved templates in the backend.
- Reconcile database state with the actual rootless Podman runtime.
- Make single-server operations recoverable through documented credential,
  backup, readiness, and lifecycle procedures before production deployment.
- Keep installation small: one Go service, SQLite, and locally served assets.

Generic proxied applications and broader file operations remain planned work. See
[PROJECT.md](PROJECT.md),
[ARCHITECTURE.md](ARCHITECTURE.md), and [ROADMAP.md](ROADMAP.md) for scope and
design decisions.

## Initial deployment

The first deployment target is one Linux server with one active COWS process,
one SQLite database, and one local rootless Podman runtime. A reverse proxy may
terminate HTTPS. COWS must not expose workspace VNC, SSH, terminal, or
application ports directly to users. The single-process assumption is
intentional: admission coordination and authentication throttles are currently
process-local, so multiple active COWS instances are unsupported. See
[docs/configuration.md](docs/configuration.md)
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

For a local rootless-Podman test instance, keep runtime data in the ignored
`.cows-test/` directory so `go test ./...` never scans mapped container data:

```sh
COWS_DATABASE_PATH=./.cows-test/cows.db \
COWS_MOUNT_ROOT=./.cows-test/cows-mounts \
COWS_MOUNT_ARCHIVE_ROOT=./.cows-test/cows-mounts-archive \
./bin/cows
```

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
