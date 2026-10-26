# Configuration and Local Operation

COWS is a single Go process. It reads defaults, then environment variables,
then command-line flags; later values win. Flags use the environment variable
names without the `COWS_` prefix and with underscores replaced by hyphens.
Passwords are exceptions: `COWS_BOOTSTRAP_ADMIN_PASSWORD` and
`COWS_SMTP_PASSWORD` are environment-only; their former CLI flags are rejected
to keep secrets out of process arguments. Protect the service environment and
avoid entering literal credentials into shell history.

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

The bootstrap variables only create the first administrator on an empty
database; they do not reset an existing one. If every administrator password is
later lost, use `./bin/cows recover-admin USERNAME` from the host holding the
database file — see "Administrator credential recovery" in
[deployment.md](deployment.md) and decision 0026.

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
| `COWS_COOKIE_SECURE` | `false` | Required `true` when the external URL is HTTPS. |
| `COWS_EXTERNAL_BASE_URL` | empty | Trusted absolute base URL for credential links. HTTPS requires Secure cookies; enabled email rejects public HTTP URLs (loopback development is allowed). |
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
| `COWS_EMAIL_WARNING_LEAD_DIVISOR` | `3` | Fraction of a workspace's timeout window used as the warning lead time. |
| `COWS_EMAIL_WARNING_LEAD_MAX` | `24h` | Upper bound on the warning lead time. |
| `COWS_EMAIL_WARNING_LEAD_MIN` | `1h` | Lower bound. A window whose lead falls below this sends no warning at all. |
| `COWS_EMAIL_RETRY_INTERVAL` | `15m` | Retry interval for failed delivery. |
| `COWS_INVITATION_LIFETIME` | `24h` | How long an account invitation link stays valid. |
| `COWS_PASSWORD_RESET_LIFETIME` | `2h` | How long a password reset link stays valid. |
| `COWS_PASSWORD_RESET_MIN_INTERVAL` | `5m` | Minimum interval between reset messages for one account. |

### Warning lead times

The lead time for a lifecycle warning is derived from the workspace's own
timeout window rather than configured directly:

```
lead = min(window / COWS_EMAIL_WARNING_LEAD_DIVISOR, COWS_EMAIL_WARNING_LEAD_MAX)
```

No warning is sent when that lead falls below `COWS_EMAIL_WARNING_LEAD_MIN`. At
`MAX * DIVISOR` the two bounds meet, so with the defaults a three-day window
yields exactly 24 hours from either direction and the function is continuous.

With the defaults, a one-hour no-connection stop window sends nothing — an
automatic stop is reversible, and a twenty-minute notice is noise. A six-hour
window warns two hours ahead; any retention window of three days or more warns
24 hours ahead.

`COWS_EMAIL_WARNING_LEAD_TIME` was removed and is now **rejected at startup**.
A deployment that sets it will not start until the key is replaced by the three
above. This is deliberate: silently changing warning behavior under a key that
still looks like it works would be worse than a failed start.

### Account invitations

An administrator can create a user without a password, in which case COWS
mails a single-use invitation link that sets the first password. This requires
both `COWS_EMAIL_ENABLED` and `COWS_EXTERNAL_BASE_URL`; without them the
password field stays mandatory and the temporary-password paths are unchanged.
The same condition gates the password-reset link on the login page and the
administrator's send-invitation and send-reset-link actions.

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
`GET /readyz` is the bounded runtime-aware readiness endpoint: it checks
SQLite and rootless-Podman connectivity and returns HTTP 503 when either is
unavailable. Point a reverse proxy's or process supervisor's readiness probe
at `/readyz`, not `/healthz`.

COWS does not create backups automatically. See
[deployment backup and restore](deployment.md#backup-and-restore) for the
current single-server procedure and its named-volume limitations.

## Rootless Podman requirements

- Podman 4.1.0 or newer. Check with `podman version`. COWS reads the version
  from the socket and refuses older releases, such as the 3.4.4 package in
  Ubuntu 22.04: they silently ignore the passwd entry COWS sends for a
  template `container_user`, so workspaces would start with the UID as their
  username. On an older Podman, `GET /readyz` reports the runtime as
  `unavailable` and COWS does not create workspaces. See
  [ADR 0028](decisions/0028-minimum-podman-version.md).
- COWS and the Podman user service run as the same Linux user.
- The configured socket is a rootless Podman user socket, not a rootful socket
  or Docker socket.
- The user has valid subordinate UID and GID ranges in `/etc/subuid` and
  `/etc/subgid` when templates configure a container identity. COWS rejects a
  non-contiguous, overlapping, or overflowing rootless mapping, and a mapping
  whose subordinate range contains the COWS service UID or GID. The file helper
  verifies the same mapping from `/proc/self/{uid,gid}_map` before dropping
  privileges; a `podman unshare` namespace that differs from the Podman service
  namespace fails closed.
- COWS-managed directory and archive roots are writable by the service user,
  are on the same filesystem, and do not contain one another.
- The SQLite database is on local supported storage, not an unsupported shared
  network filesystem.

The first workspace created from an image whose template sets a
`container_user` makes Podman build a re-owned copy of the whole image for
the private user namespace. On storage without reflink support this copies
every byte: about twenty minutes for a 14.6 GB image. Until it finishes,
Podman answers almost no API request, so COWS reports the runtime as
unavailable, and workspace starts, stops, and new terminals wait. Running
workspaces and open sessions keep working. Later workspaces from the same
image version reuse the copy, so create one workspace from a large new image
outside busy hours. COWS's first create can time out during the copy and
leave an unrecorded container behind; see the 2026-10-14 and 2026-10-15
entries in [audit findings](implementation/audit-findings.md).

COWS uses `none` networking for templates without the desktop service. A
desktop-enabled template uses loopback-only host mapping. When
`COWS_NETWORK_ISOLATION_ENABLED=true`, each newly created desktop-enabled
workspace gets a server-generated internal Podman network. COWS fails closed if
it cannot create that network. Existing workspaces must be recreated; this is
not complete host-level egress policy. Workspaces from templates with a
`container_user` that were created before 2026-10-14 were attached to Podman's
default network despite this setting and must be recreated as well. See
[SECURITY.md](../SECURITY.md).

## Production configuration

Keep COWS bound to loopback, set `COWS_COOKIE_SECURE=true`, and terminate TLS
at a trusted reverse proxy. Use [docs/deployment.md](deployment.md) and the
examples under `deploy/`. Do not expose the Podman socket, SQLite file, mount
roots, or the plain COWS listener to users.

## Security hardening upgrades

Migration 0030 adds a unique index on non-empty, case-normalized email addresses.
Existing duplicates stop migration and leave the accounts unchanged. Correct
ambiguous addresses through the previous application version before retrying;
the upgrade never chooses an account, clears an address, or deletes user data.
Back up the database before upgrading. This upgrade also retires existing
sessions and reset/invitation links, and cancels pending credential mail.
Historical login races and CSV corrections may have left them usable after
revocation. Users sign in again and request fresh links after upgrade; account
passwords and lifecycle mail are unchanged.

Migration 0031 records the actual named-volume sources used by new successful
reattachments. It cannot reconstruct source identities already lost by an older
reattachment. Inspect and recover those workspaces manually before deleting or
recreating them; ordinary legacy workspace volume naming is preserved.

Terminal-enabled images must supply trusted `/bin/kill` and its loader/libraries.
Mounts may not replace system executable/library trees or dynamic-loader files;
template and runtime validation enforce this, including standard usr-merge
aliases. A custom user shell may still live on a managed writable mount because
cleanup never executes it as root. Existing unsafe containers need operator
review; COWS does not migrate them automatically.

Signed CSRF forms are bound to the session and the current process signing key.
Reload an old tab after signing in or restarting COWS. Startup clears connection
counts left by the previous process while preserving existing idle deadlines;
workspaces whose connections were lost receive a fresh idle window.

SMTP sends have a 30-second total deadline, shortened by caller deadlines, and
cancellation closes an in-progress protocol exchange. Failed delivery remains in
the existing retrying outbox.
