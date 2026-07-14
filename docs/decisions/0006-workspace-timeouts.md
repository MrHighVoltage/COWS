# ADR 0006: Workspace Timeout Phases

Status: accepted for workspace lifecycle preparation

## Decision

COWS models workspace cleanup as two separate administrator-defined timeout
phases, initially configured on each workspace template and copied into the
workspace when it is created:

1. **Initial connection timeout**: after a workspace starts, if no authenticated
   user connection has been recorded by the deadline, COWS stops the container
   but retains it.
2. **Stopped-container retention**: after the container enters a stopped state,
   COWS may delete the container once this duration expires. The workspace
   record remains until reconciliation confirms the runtime result.
Explicit user or administrator deletion archives the managed directory tree
immediately. Automatic timeout cleanup never deletes or archives user data, so
there is no post-deletion data-retention setting.

Durations are stored as seconds with an explicit zero policy. Zero disables the
phase in the initial implementation. The effective values are copied into each
workspace so changing a template does not unexpectedly alter existing user
environments.

## User visibility and notifications

Workspace pages show the effective timeout durations, current lifecycle phase,
and known deadline. The backend remains authoritative; browser timers are only
presentation. Timeout transitions emit structured audit/lifecycle events and
leave a future notification hook for warnings before stop or deletion. Email
delivery is deliberately not implemented in this milestone.

## Safety

Timeout processing is idempotent and reconciler-driven. Ambiguous runtime or
database state must not trigger irreversible container deletion. Automatic
cleanup is intentionally conservative about user data.
