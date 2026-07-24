# COWS Roadmap

This roadmap describes the current implementation state, not a promise to
ship every future feature. A milestone is complete only when its exit criteria
and security review are complete.

## Current status

Milestone 0 is complete. Milestones 1 through 5 and 7 through 8 have useful
initial implementations, but several remain in hardening status. COWS is a
single-server, rootless-Podman system and is not production-ready.

Implemented now:

- local authentication, password changes, self-registration, CSV user import,
  groups, quotas, account lifecycle, sessions, CSRF, and basic audit events;
- typed administrator templates, resource selection, container identity,
  terminal UID policy, VNC secrets, managed mounts, image availability, pulls,
  and template copying;
- workspace lifecycle, desired/observed state, reconciliation, timeout
  processing, measured storage, overbooked host admission, and explicit data
  archival;
- authenticated terminal, noVNC desktop, restricted file manager, bounded
  uploads, and streamed directory ZIP downloads;
- optional persisted lifecycle-warning email delivery.

Not implemented or incomplete:

- complete network isolation and egress policy between workspaces;
- robust persistent recovery for every partial or interrupted lifecycle
  operation;
- the constrained web-application proxy and any other service exposure;
- password reset, email verification, OpenID Connect, and institutional
  identity provisioning;
- a complete administrator audit viewer, metrics history, and operational
  alerting;
- named-volume administrator recovery/cleanup workflows;
- archive extraction, file previews, bulk file operations, and stronger
  filesystem race/integration coverage;
- multi-host agents, host pools, PostgreSQL, high availability, GPUs, and
  shared storage;
- packaged service units, upgrade/backup tooling, and production deployment
  hardening.

## Milestone 0: Project foundation — complete

Go module, configuration, structured logging, graceful shutdown, SQLite
migrations, Go templates, embedded local assets, health endpoint, HTMX proof,
tests, dependency verification, and core architecture/security documentation.

Exit criteria: the documented build, test, vet, asset verification, and local
run commands work without Node.js, npm, or a frontend build step.

## Milestone 1: Accounts and authorization — initial implementation

Implemented: administrator bootstrap, bcrypt passwords, login/logout, opaque
sessions, CSRF protection, mandatory first-login password changes, user email
addresses, password changes, local self-registration with server defaults,
registration rate limiting, bounded CSV import with preview and credential
export, roles, groups, quotas, disable/delete safety, group lifecycle, and
basic audit persistence.

Remaining exit criteria:

- review every state-changing route and access session for independent
  authorization coverage;
- define recovery procedures for the first administrator and lost credentials;
- add broader abuse, session invalidation, and import failure tests;
- replace process-local rate limits before supporting multiple active instances;
- add a dedicated design before password reset or email verification.

## Milestone 2: Templates and runtime inspection — initial implementation

Implemented: validated typed templates, role and group access, resource policy,
runtime configuration snapshots, the COWS runtime boundary, rootless Podman
capability checks, managed labels, runtime overview, orphan observation audit,
exact local image availability, explicit image pulls, and template copying.

Remaining exit criteria:

- complete runtime connectivity and readiness reporting;
- define and test restart/reconcile behavior for missing, duplicate, orphaned,
  and partially-created objects;
- expand fake-runtime contract tests and optional rootless-Podman integration
  tests;
- add a focused administrator audit view.

Docker support is intentionally removed from the roadmap. A second runtime is
not planned until Podman behavior and isolation requirements justify it.

## Milestone 3: Workspace lifecycle and resource policy — initial implementation

Implemented: create/start/stop/restart/delete, ownership enforcement, desired
and observed state, runtime reconciliation, lifecycle operation status,
initial-connection stop timeout, stopped-container deletion timeout, explicit
directory archival, retained-volume tombstones, measured storage, user/group
quotas, running and total workspace limits, CPU/memory overbooking factors,
resource selection between template defaults and maxima, and detailed user
errors.

Remaining exit criteria:

- make lifecycle operations durable and restart-safe across every partial
  failure;
- define repair behavior for a database record without a container and an
  orphaned managed container;
- close admission races beyond the current single-process coordination;
- add irreversible-operation failure-path and archive recovery tests;
- improve administrator capacity and reconciliation diagnostics.

Storage is measured for workspace display and finite user allowances. It is not
a per-template or per-workspace runtime limit and host storage does not block
creation.

## Milestone 4: Terminal access — initial implementation

Implemented: local xterm.js, authenticated WebSocket sessions, server-selected
template shells, login-shell execution, optional template-selected terminal
UIDs, resize forwarding, idle/max lifetime limits, cleanup, audit events, and
Podman exec streaming.

Remaining hardening: rootless-Podman integration coverage, accessibility review,
session observability, and a more explicit policy for templates that allow UID
0.

## Milestone 5: Graphical desktop access — initial implementation

Implemented: local noVNC core modules, authenticated WebSocket routing,
template-controlled desktop service, loopback mapping verification, automatic
template-selected VNC credentials, session cleanup, fullscreen/resize behavior,
and no public VNC port.

Remaining hardening: rootless-Podman integration against representative VNC
images, browser accessibility review, and stronger network isolation for
desktop-enabled workspaces.

## Milestone 6: Workspace web applications — not started

Implement only a constrained, template-defined authenticated gateway for
approved internal HTTP services. It must address authorization, SSRF, allowed
ports/protocols, redirects, cookies, origins, path rewriting, WebSockets,
request/response limits, and timeouts. Do not build a generic proxy.

## Milestone 7: Resource monitoring and email — initial implementation

Implemented: live Podman CPU, memory, and PID observations, host overbooking
settings, user-visible allocation bars, timeout warning events, optional SMTP
delivery, persisted deduplication, bounded retries, and separate notification
processing.

Remaining work: richer capacity views, historical metrics only if justified,
operational alerting, and additional warning policies. Email must remain
advisory and must never decide or block lifecycle actions.

## Milestone 8: Restricted file manager — initial implementation

Implemented: approved directory and named-volume mounts, read-only/read-write
policy, rooted server-side paths, stopped-workspace access, listing, folder
creation, rename, deletion, bounded uploads, individual downloads, streamed
bounded directory ZIP downloads, rootless namespace-helper access, lifecycle
serialization, explicit archive activity logging, and storage measurement
caching.

Remaining work: named-volume administrator recovery and cleanup, stronger
symlink-race integration tests, total temporary-storage policy, file previews,
bulk operations, and archive extraction only after a dedicated security design.

## Milestone 9: Institutional authentication — not started

Evaluate OpenID Connect, account linking/provisioning, role mapping, and a
recovery-administrator strategy. Do not add email verification or password
reset as an incidental part of this milestone.

## Milestone 10: Network isolation — planned

Design and implement per-workspace/private networks or equivalent Podman
policy, explicit cross-workspace denial, controlled egress, DNS policy, and
tests proving that one workspace cannot reach another. Preserve the COWS
WebSocket access gateway and avoid public service ports.

## Later

Evaluate a privileged multi-host COWS agent, host pools, PostgreSQL, high
availability, external metrics, GPUs, shared storage, packaged service units,
backup/restore tooling, and production upgrade procedures only when real
deployment requirements justify them.
