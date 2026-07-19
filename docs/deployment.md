# COWS Deployment Preparation

COWS currently serves HTTP. For a deployment, place it behind a reverse proxy
that terminates HTTPS and keep the COWS listener reachable only from the local
machine.

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

Caddy's automatic HTTPS is intentionally not enabled in the development
environment. Do not expose a plain COWS listener publicly.

COWS does not trust browser-supplied forwarded headers and uses relative URLs,
so the proxy does not need to provide application identity or authorization
headers. Authentication and authorization remain inside COWS.

The application sets baseline response headers itself. The proxy adds HSTS,
which must only be sent after HTTPS is working correctly.
