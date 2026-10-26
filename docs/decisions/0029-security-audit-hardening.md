# ADR 0029: Security Audit Hardening

## Status

Accepted.

## Decision

Keep the existing single-process, source-address rate-limit policy. Reserve an
attempt before credential work starts, counting both in-flight requests and
completed failures. Registration and credential-link confirmation consume their
attempt even on success. This bounds parallel bcrypt work without introducing a
new account lockout or proxy trust policy.

Sign CSRF cookies with a process-local random HMAC key and bind the signature to
the session cookie. Require the same signed value in the form. Reject browser
mutations carrying a cross-origin Origin or fetch-metadata header. Do not trust
forwarded headers to choose the origin. CSRF cookies are HttpOnly because the
server renders all form tokens.

A restart invalidates previously rendered CSRF forms, and signing in requires a
fresh form for the new session. Reloading the page supplies one. CSRF key
rotation on later restarts leaves sessions valid and needs no persistent signing
secret or CSRF database migration. The one-time credential upgrade below is
separate.

Credential changes, session revocation, and credential-link retirement commit
atomically. Login and link issuance must still match the credential generation
and recipient observed before password verification or token generation. The
database enforces uniqueness of non-empty email addresses, rather than relying
on a preceding lookup. An application-derived canonical key uses the same Go
Unicode lowercase/whitespace normalization for writes, lookup, and transactional
upgrade backfill; display addresses are preserved and ordinary SQLite tools need
no custom collation. Existing duplicates fail upgrade without changing accounts.
The upgrade retires previous sessions and credential links and cancels pending
credential mail. Historical sessions do not record their credential generation;
links do not record their original recipient. Neither can be proved safe after
the former login race and CSV invalidation defects. Users sign in again and
request fresh links; account passwords and lifecycle mail are preserved.

Terminal disconnect cleanup executes fixed `/bin/kill` directly, with inherited
container environment values cleared. It never executes the configured terminal
shell as root. Typed and runtime validation reject mounts covering trusted system
executables, libraries, or dynamic-loader configuration, including standard
usr-merge aliases. Approved images must provide a trusted `/bin/kill` and its
runtime dependencies, without aliases into user-writable trees. Existing unsafe
mounts fail cleanup closed; this does not automatically migrate containers.
Cleanup remains scoped to the exec PID/process group. Independently created
process groups are not a process-tree termination guarantee.

Serve a Content-Security-Policy compatible with the existing vendored clients.
Disallow native inline script handlers, external scripts, framing, objects, base
URLs, and cross-origin form destinations. The ordinary Alpine build still needs
`unsafe-eval`, and current clients need inline styles; this policy is additional
containment, not a strict nonce-only CSP. Keep same-authority WebSocket access
working under HTTPS termination without trusting forwarded headers.

Password secrets are environment-only configuration; the bootstrap and SMTP
password CLI flags are removed so deployments cannot put them in process argv.
An HTTPS external URL requires Secure cookies. Credential email URLs require
HTTPS except for explicit loopback development.

## Security consequences

A sibling host cannot authorize a mutation by injecting a matching cookie and
form value. A reset cannot be bypassed by an old-password login completing after
session revocation. Invalid or expensive public credential requests share the
existing bounded source policy. Reverse-proxy client identity, distributed rate
limiting, storage containment, and runtime repair policy are unchanged.

## References

- [Alpine CSP compatibility](https://alpinejs.dev/advanced/csp).
- [WebSocket connect-src compatibility](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Content-Security-Policy/connect-src).
