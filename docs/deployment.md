# COWS Deployment

COWS is a single Go process with a local SQLite database and one rootless
Podman user service. The development server serves plain HTTP. For any
deployment with real users, keep COWS on loopback and place it behind a reverse
proxy that terminates HTTPS. The proxy must preserve WebSocket upgrades for
terminal and desktop sessions.

## Build and run

```sh
tools/web-assets.sh verify
go test ./...
go vet ./...
go build -o bin/cows ./cmd/cows
systemctl --user enable --now podman.socket
COWS_COOKIE_SECURE=true ./bin/cows
```

The command assumes the remaining configuration is supplied through the
environment or an operator-managed environment file. See
[configuration.md](configuration.md) for every variable, defaults, bootstrap
credentials, rootless Podman setup, and development-only remote listening.

For local password reset, configure `COWS_EMAIL_ENABLED=true` and
`COWS_EXTERNAL_BASE_URL=https://cows.example.edu` only after the reverse proxy
is serving HTTPS. The same SMTP worker delivers lifecycle warnings and reset
messages. Institutional authentication is not part of this deployment.

Use a process supervisor for a long-running installation. COWS does not ship a
systemd unit yet, so the supervisor must set the working directory, the COWS
environment, the rootless Podman socket, and restrictive file permissions.
Point the supervisor's and reverse proxy's readiness probe at `GET /readyz`,
which checks both SQLite and rootless-Podman connectivity; use `GET /healthz`
only for liveness, since it does not verify the Podman socket.

## Backup and restore

COWS does not create backups automatically. A usable single-server backup must
cover the SQLite control-plane database, the managed directory root, and the
archive root. Retained named volumes live in Podman storage and are not covered
by those files; use the administrator recovery view to download them separately
until a supported volume export workflow exists.

For a full consistent snapshot:

1. Stop new workspace operations, stop running workspaces, and stop COWS
   gracefully. Keep the database and both data roots on local supported
   storage.
2. Create a permission-restricted backup directory outside the COWS data roots.
3. Create a SQLite backup with the SQLite backup API, which handles WAL mode:

   ```sh
   install -d -m 700 /srv/backups/cows
   sqlite3 "$COWS_DATABASE_PATH" \
     ".backup '/srv/backups/cows/cows.db'"
   ```

4. Archive both managed data roots while preserving numeric ownership. Replace
   the example paths with the configured absolute paths:

   ```sh
   tar --numeric-owner -czf /srv/backups/cows/mounts.tar.gz \
     -C /srv/cows cows-mounts
   tar --numeric-owner -czf /srv/backups/cows/archive.tar.gz \
     -C /srv cows-mounts-archive
   ```

5. Verify the backup before considering it usable:

   ```sh
   sqlite3 /srv/backups/cows/cows.db 'PRAGMA integrity_check;'
   tar -tzf /srv/backups/cows/mounts.tar.gz >/dev/null
   tar -tzf /srv/backups/cows/archive.tar.gz >/dev/null
   ```

An online SQLite `.backup` is acceptable for a database-only snapshot, but it
does not make simultaneous filesystem changes in workspace mounts consistent.
For a complete recovery point, quiesce workspaces as above. Keep environment
files and SMTP credentials in a separate encrypted operator backup; never put
them in logs or an unprotected archive.

To restore, stop COWS and all affected workspaces, move the current database and
data roots aside, restore the verified database and archives with the COWS
service account's restrictive permissions, and start COWS. Check
`/readyz`, run reconciliation, inspect the Runtime view, and verify a test
workspace before returning the service to users. Do not restore only the SQLite
file while leaving its managed data roots from another point in time. Do not
manually edit account password hashes or workspace rows as a credential-recovery
procedure; offline administrator recovery is not implemented yet.

## Reverse-proxy rules

1. Bind COWS to `127.0.0.1:8080`.
2. Set `COWS_COOKIE_SECURE=true` only when the external URL is HTTPS.
3. Forward ordinary HTTP requests and WebSocket upgrades to the same upstream.
4. Keep the Podman socket, SQLite database, mount roots, and plain listener
   inaccessible from the network.
5. Add HSTS only after HTTPS is working correctly.
6. Do not rely on proxy headers for authentication or authorization; COWS
   performs those checks itself.

The examples use one-hour upstream timeouts because terminal and desktop
sessions may remain open. COWS still enforces its own session lifetime and idle
limits. The proxy examples are starting points, not complete deployment
hardening or certificate automation.

## nginx

Copy [deploy/nginx/cows.conf.example](../deploy/nginx/cows.conf.example) into
the nginx configuration, move its `map` block into the `http {}` context,
replace the hostname and certificate paths, and reload nginx. Ensure nginx
has permission to connect to the loopback listener. `proxy_buffering off` is
intentional for interactive access and streamed file downloads.

## Apache httpd

Copy [deploy/apache/cows.conf.example](../deploy/apache/cows.conf.example),
replace the hostname and certificate paths, and enable `mod_proxy`,
`mod_proxy_http`, `mod_ssl`, and `mod_headers` before reloading Apache. The
example uses Apache 2.4.47+'s WebSocket upgrade support in `mod_proxy_http`.
On older Apache versions, enable `mod_proxy_wstunnel` and replace the single
`ProxyPass` with specific `ProxyPassMatch` rules for the terminal and desktop
`/ws` endpoints before the ordinary HTTP `ProxyPass`.

Apache and nginx must not be configured as open forward proxies. Their only
upstream should be the local COWS listener.

## Caddy

The repository contains a starting example at
`deploy/caddy/Caddyfile.example`. Before using it:

1. Replace `cows.example.org` with the real DNS name.
2. Run COWS on loopback, for example with `COWS_LISTEN_ADDR=127.0.0.1:8080`.
3. Set `COWS_COOKIE_SECURE=true`.
4. Confirm that the reverse proxy passes WebSocket upgrades for terminal and
   desktop access.
5. Protect the COWS data directory, SQLite database, and rootless Podman
   socket with the service account's filesystem permissions.

COWS does not trust browser-supplied forwarded headers and uses relative URLs,
so the proxy does not need to provide application identity or authorization
headers. Authentication and authorization remain inside COWS.

The application sets baseline response headers itself. The proxy examples add
HSTS, which must only be sent after the TLS proxy is working correctly.
