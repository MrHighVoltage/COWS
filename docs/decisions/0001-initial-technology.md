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
| Authentication | Local users in the current scope | Local bootstrap and recovery are useful for the initial deployment. Passwords use bcrypt through `golang.org/x/crypto`, and new users must change their initial password. No custom token format. External identity providers are out of scope for the current project. |
| Sessions | Server-side opaque sessions in SQLite | Cookies carry only an opaque identifier and can be revoked. Session records store token hashes, not browser tokens. |
| Authorization | Explicit service and handler checks, roles first | Makes administrator and fail-closed behavior visible before a larger permission model. |
| CSRF | Per-browser token cookie plus hidden form token | State-changing form requests require a constant-time token match. SameSite cookies are defense in depth, not the sole control. |
| WebSockets | `github.com/coder/websocket` v1.8.12 | Focused Go WebSocket implementation; the standard library has no WebSocket server. ISC license. It is a runtime dependency with no Node.js or npm requirement. |
| SSE | `net/http` streaming later | One-way updates need no new dependency. |
| Runtime | Rootless Podman adapter only for the initial deployment | Avoids a second runtime surface while preserving the COWS-facing adapter boundary. |
| UI component library | Web Awesome evaluated, deferred | Its `dist-cdn` self-hosting mode can avoid a build tool, but the project’s documented installation path is npm-oriented and the complete vendored distribution/licensing update process still needs verification. Milestone 0 uses semantic HTML/CSS. |
| HTMX | 2.0.10, vendored locally | Dependency-free browser library with direct downloadable browser distribution and no build requirement. |
| Alpine.js | 3.14.9, vendored locally | Small browser-local behavior with a direct script distribution; not authoritative state. |
| xterm.js | xterm.js 5.3.0, vendored locally, with a locally embedded JetBrains Mono Nerd Font subset | Specialized terminal rendering should not be recreated. The embedded OFL-1.1 font subset gives browser terminals consistent modern monospace text and Powerline glyphs without client installation. MIT license, browser runtime only, no Node.js or npm requirement in COWS builds. |
| noVNC | noVNC 1.6.0, vendored locally | Specialized VNC browser client; only the core ES modules are used. MPL-2.0 license, browser runtime only, no Node.js or npm requirement in COWS builds. |
| Tests | Go testing, `httptest`, temporary SQLite, and optional rootless Podman integration tests | Ordinary tests remain independent of a runtime; live tests validate the supported rootless deployment. |
| Formatting/static analysis | `gofmt`, `go vet`; optional `staticcheck` | Built-in tools are available everywhere; Staticcheck is useful when adopted by CI. |

## Dependency and asset policy

Go dependencies are pinned by `go.mod`/`go.sum`. Browser assets are checked into
`web/static/vendor`, accompanied by source URLs, exact versions, licenses, and
checksums in `web/static/vendor/README.md`. Production pages do not load a CDN.
The complete lock file is `web/static/assets.lock` and `tools/web-assets.sh`
provides offline verification and deliberate refreshes. Updates must be
reviewed: inspect release notes and license changes, review lock-file checksum
changes, and run the full verification commands.

## Sources consulted

- [Go release history](https://go.dev/doc/devel/release)
- [HTMX documentation](https://htmx.org/docs/)
- [Alpine.js installation](https://alpinejs.dev/essentials/installation)
- [Web Awesome installation](https://webawesome.com/docs/)
