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

Use a process supervisor for a long-running installation. COWS does not ship a
systemd unit yet, so the supervisor must set the working directory, the COWS
environment, the rootless Podman socket, and restrictive file permissions.

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
