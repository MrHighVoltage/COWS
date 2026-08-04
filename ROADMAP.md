# COWS Roadmap

This roadmap describes the current implementation state, not a promise to
ship every future feature. A milestone is complete only when its exit criteria
and security review are complete.

## Current status

Milestone 0 is complete. Milestones 1 through 5 and 7 through 9 have useful
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

- offline administrator credential recovery and a documented lost-credential
  procedure;
- SQLite, managed-directory, archive, and retained-volume backup/restore
  procedures and verification;
- a runtime-aware readiness endpoint; `/healthz` currently checks liveness and
  database connectivity only;
- stronger host-level network egress policy between workspaces;
- robust persistent recovery for every partial or interrupted lifecycle
  operation;
- a defined, administrator-gated repair policy for missing records, orphaned
  containers, and interrupted operations;
- build-tagged rootless-Podman integration coverage and broader abuse,
  session-invalidation, import-failure, and filesystem race tests;
- arbitrary application/service exposure; this is intentionally out of scope;
- email verification, OpenID Connect, and institutional identity provisioning;
- metrics history and operational alerting beyond the live administrator view;
- named-volume restore/reattachment beyond administrator recovery/download;
- archive extraction, file previews, bulk file operations, and stronger
  filesystem race/integration coverage;
- multi-host agents, host pools, PostgreSQL, high availability, GPUs, and
  shared storage;
- packaged service units, upgrade tooling, and production deployment hardening.

## Current priority queue

These are the next pre-production tasks, ordered by operational risk:

1. Add an operator-invoked local administrator recovery command that targets a
   named administrator, invalidates sessions, generates a temporary password,
   and requires a first-login password change. Refuse unknown, disabled, and
   non-administrator targets. Document local-process and database-file access
   requirements.
2. Publish and test the SQLite, managed-directory, archive, and retained-volume
   backup/restore procedure. Keep named-volume backup separate from the
   control-plane backup until a supported export path exists.
3. Separate liveness from readiness and make readiness check both SQLite and
   rootless-Podman connectivity with a bounded timeout.
4. Define restart recovery and non-destructive repair policy for every lifecycle
   partial failure before adding automatic repair actions.
5. Add opt-in rootless-Podman integration tests for labels, lifecycle state,
   terminal cleanup, desktop loopback mapping, private-network setup, and
   rooted file access. Keep them out of the ordinary unit-test suite.

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
- implement and document local recovery for the first administrator and lost
  credentials;
- add broader abuse, session invalidation, and import failure tests;
- replace process-local rate limits before supporting multiple active instances;
- add email verification only after a separate identity design.

## Milestone 2: Templates and runtime inspection — initial implementation

Implemented: validated typed templates, role and group access, resource policy,
runtime configuration snapshots, the COWS runtime boundary, rootless Podman
capability checks, managed labels, runtime overview, orphan observation audit,
exact local image availability, explicit image pulls, and template copying.

Remaining exit criteria:

- complete runtime connectivity and readiness reporting;
- define and test restart/reconcile behavior for missing, duplicate, orphaned,
  and partially-created objects;
- expand fake-runtime contract tests and add optional rootless-Podman integration
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
- define administrator-gated, non-destructive repair behavior for a database
  record without a container, an orphaned managed container, and a partially
  completed operation;
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

Remaining hardening: tagged rootless-Podman integration coverage, accessibility
review, session observability, and a more explicit policy for templates that
allow UID 0.

## Milestone 5: Graphical desktop access — initial implementation

Implemented: local noVNC core modules, authenticated WebSocket routing,
template-controlled desktop service, loopback mapping verification, automatic
template-selected VNC credentials, session cleanup, fullscreen/resize behavior,
and no public VNC port.

Remaining hardening: tagged rootless-Podman integration against representative
VNC images, browser accessibility review, and stronger network isolation for
desktop-enabled workspaces.

## Milestone 6: Resource monitoring and email — initial implementation

Implemented: live Podman CPU, memory, and PID observations, host overbooking
settings, user-visible allocation bars, timeout warning events, optional SMTP
delivery, persisted deduplication, bounded retries, and separate notification
processing.

Remaining work: richer capacity views, historical metrics only if justified,
operational alerting, and additional warning policies. Email must remain
advisory and must never decide or block lifecycle actions.

## Milestone 7: Restricted file manager — initial implementation

Implemented: approved directory and named-volume mounts, read-only/read-write
policy, rooted server-side paths, stopped-workspace access, listing, folder
creation, rename, deletion, bounded uploads, individual downloads, streamed
bounded directory ZIP downloads, rootless namespace-helper access, lifecycle
serialization, explicit archive activity logging, and storage measurement
caching.

Remaining work: stronger symlink-race integration tests, total temporary-storage
policy, file previews,
bulk operations, and archive extraction only after a dedicated security design.

## Milestone 8: Local password reset and operations visibility — initial implementation

Local password-reset email with hashed single-use tokens, a retryable email
outbox, a bounded administrator audit view, live runtime metrics, and
administrator retained-volume recovery/download/removal. Institutional
authentication is deliberately excluded from the current plan. This does not
provide offline administrator credential recovery; that remains in Milestone 1.

## Milestone 9: Optional network isolation — implemented

New desktop-enabled workspaces can use server-generated internal per-workspace
Podman networks. Creation fails closed when isolation is enabled but the
runtime cannot provide it. Stronger host-level egress policy and migration of
existing workspaces remain future work.

## Later

Evaluate a privileged multi-host COWS agent, host pools, PostgreSQL, high
availability, external metrics, GPUs, shared storage, packaged service units,
and production upgrade procedures only when real deployment requirements
justify them. Backup and restore are no longer a later-only feature: the
single-server deployment needs a documented procedure before production use.
