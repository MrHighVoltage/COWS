# End-to-end user-flow test plan

This document describes the opt-in end-to-end suite in `internal/e2e`, what it
covers today, how to run it safely, and which user-visible risks and
bottlenecks it is meant to expose. The ordinary unit suite never requires
Podman; these tests live behind the `e2e` build tag.

## Goals

- Exercise the same HTTP forms, redirects, cookies, CSRF checks, multipart
  uploads, downloads, and WebSockets a browser uses.
- Run against a real rootless Podman adapter, not a fake runtime, so container
  creation, mounts, the file helper, terminal execs, and cleanup are real.
- Run several independent users concurrently to expose shared bottlenecks
  (admission, SQLite, Podman API, per-workspace file locks).
- Produce a machine-readable per-operation latency/error report so a failure
  or a slow path can be compared across runs.
- Cover the irreversible and easy-to-get-wrong flows: quota exhaustion,
  retained-storage reattachment, explicit deletion and discarding, server
  restart, and terminal disconnect cleanup.

## Safety and prerequisites

- A locally present image with `/bin/sh`, `/bin/sleep`, `/bin/kill`, and the
  usual coreutils. No image is pulled by the tests.
- A rootless Podman user socket. The tests never use a rootful or Docker
  socket.
- A disposable data root outside the repository. Each test creates a temporary
  directory, a new SQLite database, separate mount and archive roots, and a
  cows process on a free loopback port.
- The suite deletes only workspaces recorded in its own database, removes only
  retained storage created by that instance, and removes its temporary root
  through `podman unshare` so subordinate-owned files can be deleted.
- Do not point `COWS_E2E_SOCKET` at a Podman service that also hosts
  non-test workspaces when running destructive scenarios.

Required environment:

```sh
# Use an image that is already present locally.
export COWS_E2E_IMAGE=docker.io/library/ubuntu:26.04

# Optional overrides:
export COWS_E2E_SOCKET=/run/user/$UID/podman/podman.sock
export COWS_E2E_USERS=4              # TestParallelUsers
export COWS_E2E_QUOTA_USERS=2        # TestParallelQuotaExhaustion
export COWS_E2E_LOGIN_USERS=8        # TestParallelLoginBurstDiagnostic
```

Run the whole suite:

```sh
COWS_E2E_IMAGE=docker.io/library/ubuntu:26.04 \
  go test -tags e2e -count=1 -v ./internal/e2e
```

Run one scenario:

```sh
COWS_E2E_IMAGE=docker.io/library/ubuntu:26.04 \
  go test -tags e2e -count=1 -run TestWorkspaceLifecycleFilesAndTerminal -v ./internal/e2e
```

## Harness architecture

`internal/e2e` builds `./cmd/cows` once per test process and starts it as a real
subprocess. The test process prepares a temporary SQLite database with:

- one bootstrap administrator whose password has already been changed;
- one enabled template with a named-volume mount (`data`), a managed-directory
  mount (`home`), terminal/file/log access, and a TERM-aware long-running
  command;
- optionally, N ordinary users with explicit quotas.

The fixture code uses the domain services only to create that starting state.
Every operation under test then goes through the running HTTP server:

- login and CSRF token extraction from the rendered HTML;
- workspace create/start/stop/restart/delete form posts;
- multipart file upload and streamed download/ZIP routes;
- terminal WebSocket connect, command, disconnect, and reconnect;
- the storage page's retained-volume and retained-directory discard actions;
- the administrator quota form.

The server subprocess also runs the ordinary reconciliation and timeout loops,
so the suite observes the same startup connection reset and background workers
as a normal deployment.

Each test writes a JSON report under
`.cows-test/e2e-reports/<test>-<timestamp>.json` (ignored by Git). The report
contains:

- every operation with user, start time, duration in milliseconds, success,
  expected-failure marker, and error/detail;
- a per-operation summary with count, unexpected errors, expected failures,
  p50, p95, and maximum latency.

`go test -v` prints the same summary. A failed test still writes the report
from a cleanup hook.

## Scenario matrix

| Test | User-visible flow | Main assertions |
| --- | --- | --- |
| `TestWorkspaceLifecycleFilesAndTerminal` | Create, start, upload/download, mkdir/rename/delete/ZIP, terminal, disconnect/reconnect, stop, restart, delete, discard storage | Container starts; both mount types round-trip bytes; ZIP contains uploaded entries; deleted files disappear; terminal cleanup leaves no login shell; restart preserves data; retained volume and directory appear and can be discarded |
| `TestAdminQuotaExhaustion` | Administrator changes quota; user creates two stopped workspaces, hits total quota, starts one, hits running quota, stops it and starts the other | Admin form update takes effect; second create succeeds before either workspace starts; third create is rejected without a container; one of two starts is rejected; start after freeing the slot succeeds |
| `TestStorageReattachmentOwnershipAndSingleUse` | Delete a workspace with a named volume and a managed directory; foreign owner attempts reattachment; owner reattaches both; same tombstone is reused | Foreign owner gets a generic rejection; owner sees the old bytes in both mounts; tombstones disappear after consumption; a second reattachment fails |
| `TestServerRestartPreservesSessionAndData` | Create/start/upload, restart the cows process, reuse the browser session, download, open a new terminal, delete | Session cookie and workspace data survive; the workspace is not wrongly stopped by startup; terminal reconnect works |
| `TestRegistrationAndAdminTemplateUI` | Administrator creates a template through the browser form; a self-registered user signs in, creates a workspace from it, uploads/downloads, and deletes | Template form validation and JSON configuration are accepted; registration applies the configured default quota; a newly created template is usable by an ordinary user |
| `TestParallelUsers` | N users concurrently create, start, upload/download both mount types, run a terminal, stop, and delete | Every user's independent flow succeeds; per-operation p50/p95/max is recorded under contention |
| `TestParallelQuotaExhaustion` | N users concurrently create two workspaces, hit the total quota, start one, hit the running quota, free the slot, and start the other | Quotas remain per-user under parallel load; rejected starts do not consume runtime resources |
| `TestParallelLoginBurstDiagnostic` | N simultaneous sign-ins from the same direct peer | Records how many requests the process-local direct-peer limiter throttles; does not fail on the documented 429 response |

## Parallel workload model

`TestParallelUsers` logs each user in sequentially (avoiding the per-peer login
limiter during setup), then launches one parallel subtest per user behind a
shared start barrier. Each subtest has its own cookie jar and HTTP client. The
container and file operations for all users therefore overlap:

- workspace creation and the global admission slot;
- Podman create/start/stop/delete API calls;
- two file-helper invocations per mount for upload/download;
- independent terminal exec sessions;
- SQLite writes for operations, usage, audit, and connection state.

`TestParallelQuotaExhaustion` uses the same model with a smaller user count and
focuses on quota isolation. `TestParallelLoginBurstDiagnostic` intentionally
sends concurrent logins from the same peer to measure the direct-peer limiter.

The suite does not use `t.Parallel` across top-level tests. Each top-level test
owns a separate database, server process, and Podman objects; running all of
them at once would multiply container pressure and make latency attribution
meaningless.

## Metrics and interpretation

Use the JSON report and the `go test -v` table to compare runs:

- `workspace.create` and `workspace.start/stop` include the global admission
  lock, the scheduler, and Podman. A rising p95 under more users indicates the
  process-wide admission path, not browser overhead.
- `file.*` includes a `podman unshare` process and the rooted file helper.
  Roughly 0.7-1.2 s per operation on the development host is a useful baseline;
  a much higher p95 under parallel load points at helper startup, filesystem
  work, or the per-workspace file lock.
- `terminal.*` includes Podman exec creation and cleanup. Compare the
  disconnect-to-cleanup time with the terminal operation latency; a leftover
  `sh -l` process is a hard failure.
- `parallel.quota.*` expected failures should never produce an unexpected
  container. Check `podman ps -a --filter label=cows.managed=true` for a test
  socket after a failed run if cleanup itself failed.
- Login burst throttling is diagnostic. The limiter is intentionally
  process-local and keyed on the direct peer, so all users behind one reverse
  proxy share a bucket.

Add thresholds only after collecting a baseline for the intended test host.
A useful next step is a `COWS_E2E_MAX_P95_MS` option that fails a scenario when
a named operation exceeds an operator-selected budget.

## Findings from the first implementation pass

### Named-volume storage measurement before first start — fixed

Creating a second workspace while the first was still stopped failed with the
generic "The workspace could not be created." The quota check measured existing
workspace storage, but Podman creates a named volume with the first container,
not with the workspace row. The file helper therefore reported the volume as
missing and the scheduler failed closed.

Fix: `StorageUsageProvider.measure` skips named-volume mounts while
`RuntimeID` is empty, because a never-started workspace cannot have volume data
yet. Directory mounts are still measured. A regression test is in
`internal/workspace/storage_test.go`; the e2e quota scenario deliberately
creates both workspaces before starting either one.

### Stop grace period vs adapter HTTP timeout — open

A template whose PID 1 ignores SIGTERM (for example a bare `sleep 3600`) takes
the full ten-second Podman stop grace period. The adapter's general HTTP client
timeout is also ten seconds, so the stop request can time out just after Podman
has finished killing the container. The UI then reports the rootless runtime as
unavailable and leaves the row marked "Stop failed" even though the container
is gone.

The e2e fixture uses a TERM-aware PID 1 so normal scenarios are stable. This
remains a production hardening item: give the stop request a deadline greater
than the requested grace period (for example `timeout + 5s`) or use a
stop-specific client/context. A dedicated e2e scenario should pin the expected
behavior once the fix lands.

### Direct-peer login limiter — expected limitation

An eight-way simultaneous login from one loopback peer throttled three
requests with HTTP 429. This matches the documented process-local,
direct-peer-based design. It becomes a user-facing bottleneck when a reverse
proxy makes every user share one peer address. A future change should either
derive the key from a trusted proxy boundary or move the limiter upstream.

### Global admission and Podman start latency — measured

On the development host, four concurrent users produced roughly 4.6 s p50 /
5.5 s p95 for start, versus about 2.9 s for a single user. The process-wide
admission channel and Podman startup dominate. This is expected for the
single-process deployment model; the report makes the serialization visible
instead of hiding it in an aggregate test duration.

## Not yet covered

These are deliberate next steps, not claims that the flows are safe:

- Admin template editing, user management, CSV import, and the full
  invitation/password-reset email-outbox flow. Template creation and
  self-registration themselves are covered by
  `TestRegistrationAndAdminTemplateUI`.
- Desktop/noVNC access; it needs a template-approved VNC image.
- Short timeout policies and the timeout worker (the suite uses one-hour
  windows so tests do not race the worker).
- File-upload size limits, read-only mount mutation rejection, and traversal
  probes through the web UI (unit coverage exists).
- Reattachment of the same tombstone by two truly simultaneous creates.
- Cross-user terminal/file authorization attempts.
- Crash/chaos cases: kill the server while a terminal is open, kill it during
  create/delete, and verify startup reconciliation and orphan handling.
- Storage-quota exhaustion, group-derived quotas, and host-capacity exhaustion.
- Audit-event assertions after each destructive action.

## CI guidance

Use a dedicated rootless account and a dedicated Podman socket. Pre-pull the
test image. Do not run these tests against a developer's normal COWS database or
Podman store. A typical CI job is:

```sh
podman pull docker.io/library/ubuntu:26.04
COWS_E2E_IMAGE=docker.io/library/ubuntu:26.04 \
COWS_E2E_SOCKET=/run/user/$(id -u)/podman/podman.sock \
  go test -tags e2e -count=1 -timeout 20m ./internal/e2e
```

Keep the ordinary `go test ./...` job separate; it must remain Podman-free.
