# ADR 0024: Runtime-Aware Readiness Endpoint

## Status

Accepted

`GET /healthz` keeps its existing liveness-plus-SQLite-connectivity meaning
and response shape; it is not changed. A new, separate `GET /readyz`
endpoint adds a bounded rootless-Podman connectivity check on top of the
same SQLite check, for a reverse proxy or process supervisor deciding
whether to admit traffic. A separate route was chosen over widening
`/healthz` so existing liveness probes keep passing during a runtime outage
that a process restart cannot fix, and so the two checks stay independently
documented and testable.

`/readyz` calls the runtime adapter's existing `Name` method (a Podman
`/info` round trip that also validates rootless mode) under a five-second
context timeout, shorter than the adapter's own ten-second HTTP client
timeout, so a stalled Podman socket cannot make the probe hang past a
typical supervisor probe interval. A `nil` runtime (only possible if COWS is
misconfigured to run without one) reports readiness as unavailable rather
than panicking, matching the existing nil-safety in `runtime.Inspect`.

`/readyz` is unauthenticated, like `/healthz`: a process supervisor or
reverse proxy cannot authenticate, and the response reveals only three
constant status words (`ok`/`degraded`, `ok`/`unavailable` per dependency),
no runtime or filesystem detail. The endpoint returns HTTP 503 when either
dependency is unavailable, and 200 otherwise.
