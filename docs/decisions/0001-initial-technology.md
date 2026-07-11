# ADR 0001: Initial Technology Choices

Status: accepted for Milestone 0

## Decisions

| Area | Choice | Reason |
| --- | --- | --- |
| Language | Go 1.26 | Current supported Go release; conventional tooling and static binaries. Pin the minimum in `go.mod` and update deliberately. |
| HTTP router | `net/http` patterns | Go 1.22+ method/path patterns cover the initial routes without a framework. |
| Middleware | Small local functions | Keeps ordering and security behavior visible; no dependency is justified yet. |
| Configuration | Standard library flag plus environment overrides | Small installation surface and clear validation. A config-file parser is deferred until needed. |
| Logging | `log/slog` | Structured logging is in the standard library. |
| SQLite | `modernc.org/sqlite` | Pure-Go SQLite driver avoids CGO and keeps the binary easy to deploy. Its resolved version is pinned in `go.mod`. |
| Migrations | Embedded ordered SQL with `schema_migrations` | Explicit, reviewable, and sufficient for one control-plane instance. No migration framework is needed yet. |
| Queries | `database/sql` and explicit SQL | Keeps schema and transaction behavior visible. `sqlc` remains an evaluation option if repetition grows. |
| Templates | `html/template` with layouts, pages, components, fragments | Escaping is correct by default and fits server-driven HTMX interactions. |
| Authentication | Local users first, OpenID Connect later | Local bootstrap and recovery are useful for initial deployment; institutional OIDC is a later milestone. Passwords use bcrypt through `golang.org/x/crypto`, and new users must change their initial password. No custom token format. |
| Sessions | Server-side opaque sessions in SQLite | Cookies carry only an opaque identifier and can be revoked. Session records store token hashes, not browser tokens. |
| Authorization | Explicit service and handler checks, roles first | Makes administrator and fail-closed behavior visible before a larger permission model. |
| CSRF | Per-browser token cookie plus hidden form token | State-changing form requests require a constant-time token match. SameSite cookies are defense in depth, not the sole control. |
| WebSockets | `github.com/coder/websocket` later | Focused, maintained Go WebSocket implementation; standard library has no WebSocket server. Exact version is selected when terminal/desktop work starts. |
| SSE | `net/http` streaming later | One-way updates need no new dependency. |
| Runtime | Separate Docker and Podman adapters later | Avoids leaking runtime objects into COWS domain code; adapter contracts will be tested with fakes. |
| UI component library | Web Awesome evaluated, deferred | Its `dist-cdn` self-hosting mode can avoid a build tool, but the project’s documented installation path is npm-oriented and the complete vendored distribution/licensing update process still needs verification. Milestone 0 uses semantic HTML/CSS. |
| HTMX | 2.0.10, vendored locally | Dependency-free browser library with direct downloadable browser distribution and no build requirement. |
| Alpine.js | 3.14.9, vendored locally | Small browser-local behavior with a direct script distribution; not authoritative state. |
| xterm.js/noVNC | Deferred to their milestones | Specialized libraries are appropriate, but vendoring unused code adds supply-chain and review surface. |
| Tests | Go testing, `httptest`, temporary SQLite | Standard library covers initial behavior without requiring Docker or Podman. |
| Formatting/static analysis | `gofmt`, `go vet`; optional `staticcheck` | Built-in tools are available everywhere; Staticcheck is useful when adopted by CI. |

## Dependency and asset policy

Go dependencies are pinned by `go.mod`/`go.sum`. Browser assets are checked into
`web/static/vendor`, accompanied by source URLs, exact versions, licenses, and
checksums in `web/static/vendor/README.md`. Production pages do not load a CDN.
Updates must be deliberate: review release notes and license changes, replace
the local asset, recompute its SHA-256 checksum, and run the full verification
commands.

## Sources consulted

- [Go release history](https://go.dev/doc/devel/release)
- [HTMX documentation](https://htmx.org/docs/)
- [Alpine.js installation](https://alpinejs.dev/essentials/installation)
- [Web Awesome installation](https://webawesome.com/docs/)
