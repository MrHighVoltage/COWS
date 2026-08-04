# Configuration and Local Operation

COWS is a single Go process. It reads defaults, then environment variables,
then command-line flags; later values win. Flags use the environment variable
names without the `COWS_` prefix and with underscores replaced by hyphens.

## Build and run

```sh
tools/web-assets.sh verify
go test ./...
go vet ./...
go build -o bin/cows ./cmd/cows
```

Start the rootless Podman user socket first:

```sh
systemctl --user enable --now podman.socket
systemctl --user status podman.socket
```

Then start COWS with an initial administrator on a new database:

```sh
export COWS_HOST_STORAGE_BYTES=$((100 * 1024 * 1024 * 1024))
export COWS_BOOTSTRAP_ADMIN_USERNAME=admin
export COWS_BOOTSTRAP_ADMIN_PASSWORD='replace-this-with-a-long-password'
./bin/cows
```

For local rootless-Podman testing, keep the live database and mapped mount
directories in the ignored `.cows-test/` directory. This prevents `go test
./...` from traversing runtime-owned directories:

```sh
COWS_DATABASE_PATH=./.cows-test/cows.db \
COWS_MOUNT_ROOT=./.cows-test/cows-mounts \
COWS_MOUNT_ARCHIVE_ROOT=./.cows-test/cows-mounts-archive \
./bin/cows
```

Use a separate test database when resetting the test environment. Do not use
these paths for a production installation.

Open `http://127.0.0.1:8080`. The default listener is local-only. For a
development-only test from another machine, use an explicit listener such as
`COWS_LISTEN_ADDR=0.0.0.0:8081`; this is plain HTTP and must not be used for
untrusted users or real credentials.

Bootstrap credentials are used only when the database has no users. Keep the
database, mount roots, and any environment file containing credentials readable
only by the COWS service account. There is currently no offline administrator
recovery command; changing bootstrap environment variables does not affect an
existing database. Configure and test local password-reset email or keep a
separate operational recovery plan.

COWS does not install a systemd unit yet. For a long-running installation, run
the binary under a process supervisor with the same working directory and
environment, and enable lingering for the account that owns the rootless
Podman service if it must run without an interactive login.

## Environment variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `COWS_LISTEN_ADDR` | `127.0.0.1:8080` | HTTP listen address. |
| `COWS_DATABASE_PATH` | `./data/cows.db` | SQLite database on local supported storage. |
| `COWS_MOUNT_ROOT` | `./data/cows-mounts` | Managed directory-mount root. |
| `COWS_MOUNT_ARCHIVE_ROOT` | `./data/cows-mounts-archive` | Sibling archive root for explicit deletion. |
| `COWS_PODMAN_SOCKET` | `/run/user/<uid>/podman/podman.sock` | Rootless Podman user socket. |
| `COWS_HOST_STORAGE_BYTES` | `0` | Initial host-storage value shown in Settings; zero means unknown. It is not currently a host storage admission check. |
| `COWS_HOST_CPU_OVERBOOKING_FACTOR` | `1` | Initial CPU admission factor, from `0.1` to `1000`. |
| `COWS_HOST_MEMORY_OVERBOOKING_FACTOR` | `1` | Initial memory admission factor, from `0.1` to `1000`. |
| `COWS_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `COWS_SHUTDOWN_TIMEOUT` | `10s` | Graceful HTTP shutdown timeout. |
| `COWS_SESSION_LIFETIME` | `8h` | Maximum authenticated session lifetime. |
| `COWS_COOKIE_SECURE` | `false` | Set `true` when the external URL is HTTPS. |
| `COWS_EXTERNAL_BASE_URL` | empty | Trusted absolute HTTP(S) base URL used for local password-reset links. Configure the HTTPS proxy URL before enabling reset email. |
| `COWS_NETWORK_ISOLATION_ENABLED` | `false` | Give new desktop-enabled workspaces separate internal Podman networks. Existing workspaces are not migrated. |
| `COWS_BOOTSTRAP_ADMIN_USERNAME` | empty | Initial administrator username. Must be paired with the password. |
| `COWS_BOOTSTRAP_ADMIN_PASSWORD` | empty | Initial administrator password. |
| `COWS_REGISTRATION_ENABLED` | `false` | Enable local self-registration. |
| `COWS_REGISTRATION_DEFAULT_GROUPS` | empty | Comma-separated existing groups for new registrants. |
| `COWS_REGISTRATION_DEFAULT_CPU_MILLIS` | `2000` | Default self-registration CPU quota. |
| `COWS_REGISTRATION_DEFAULT_MEMORY_BYTES` | `4294967296` | Default self-registration memory quota. |
| `COWS_REGISTRATION_DEFAULT_STORAGE_BYTES` | `21474836480` | Default measured-storage allowance. |
| `COWS_REGISTRATION_DEFAULT_MAX_WORKSPACES` | `2` | Default total workspace quota. |
| `COWS_REGISTRATION_DEFAULT_MAX_RUNNING_WORKSPACES` | `1` | Default running workspace quota. |
| `COWS_EMAIL_ENABLED` | `false` | Enable optional lifecycle warning and local password-reset email delivery. |
| `COWS_SMTP_HOST` | empty | SMTP server hostname. Required when email is enabled. |
| `COWS_SMTP_PORT` | `587` | SMTP server port. |
| `COWS_SMTP_FROM` | empty | SMTP sender address. |
| `COWS_SMTP_USERNAME` | empty | Optional SMTP username. |
| `COWS_SMTP_PASSWORD` | empty | Optional SMTP password. |
| `COWS_SMTP_REQUIRE_TLS` | `true` | Require STARTTLS unless using a trusted local relay. |
| `COWS_EMAIL_WARNING_LEAD_TIME` | `24h` | Lead time for timeout warning messages. |
| `COWS_EMAIL_RETRY_INTERVAL` | `15m` | Retry interval for failed warning delivery. |

Startup host settings seed the persistent Settings row only when it does not
exist. Administrators can change host storage, reserved storage, and CPU and
memory overbooking in the web UI without restarting COWS. Host storage and
reserved storage are informational and do not block workspace creation.
Overbooking above `1.0` can exhaust the host, especially for memory.

User and group storage quotas use measured workspace storage. CPU and memory
allocation quotas count running workspaces; total and running workspace-count
limits are separate. Zero means unlimited for a configured quota, while an
ordinary user with no explicit or inherited quota remains unassigned.

`GET /healthz` is a liveness and SQLite-connectivity check. It does not verify
the Podman socket, so it is not a readiness signal for admitting workspaces.
A bounded runtime-aware readiness endpoint is planned.

COWS does not create backups automatically. See
[deployment backup and restore](deployment.md#backup-and-restore) for the
current single-server procedure and its named-volume limitations.

## Rootless Podman requirements

- COWS and the Podman user service run as the same Linux user.
- The configured socket is a rootless Podman user socket, not a rootful socket
  or Docker socket.
- The user has valid subordinate UID and GID ranges in `/etc/subuid` and
  `/etc/subgid` when templates configure a container identity.
- COWS-managed directory and archive roots are writable by the service user,
  are on the same filesystem, and do not contain one another.
- The SQLite database is on local supported storage, not an unsupported shared
  network filesystem.

COWS uses `none` networking for templates without the desktop service. A
desktop-enabled template uses loopback-only host mapping. When
`COWS_NETWORK_ISOLATION_ENABLED=true`, each newly created desktop-enabled
workspace gets a server-generated internal Podman network. COWS fails closed if
it cannot create that network. Existing workspaces must be recreated; this is
not complete host-level egress policy. See
[SECURITY.md](../SECURITY.md).

## Production configuration

Keep COWS bound to loopback, set `COWS_COOKIE_SECURE=true`, and terminate TLS
at a trusted reverse proxy. Use [docs/deployment.md](deployment.md) and the
examples under `deploy/`. Do not expose the Podman socket, SQLite file, mount
roots, or the plain COWS listener to users.
