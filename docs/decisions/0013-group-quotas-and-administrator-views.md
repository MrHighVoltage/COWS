# ADR 0013: Group quotas and administrator workspace views

## Status

Accepted.

## Decision

Users may have an explicit quota row. When it exists, it is the complete quota
for that user and group quotas are ignored. When no explicit row exists, the
quotas of all groups assigned to the user are combined by resource. Finite
limits add together; a zero limit makes that resource unlimited in the
combined result. A user with no explicit quota and no assigned group quota is
unassigned and cannot create workspaces. Administrators without either type
of quota remain unrestricted, preserving the existing administrator policy.
Administrators can remove an explicit user quota to return that user to group
inheritance. Removing a group quota makes that group contribute no quota; it is
distinct from a zero-valued quota, which grants unlimited quota for each zero
resource.

The administrator Users page shows group badges and links to a dedicated user
edit page. Group membership editing is not embedded in every user table row.
Explicit user quotas are edited from that user edit page. Group quotas are
edited from a dedicated group edit page linked from the Groups table; there is
no separate global quota list to scan as the account count grows.

The Workspaces page always lists only the authenticated user's workspaces,
including for administrators. The Runtime page is the administrator view for
all observed managed containers and joins them with all COWS workspace records.
Runtime controls continue to use workspace IDs and ordinary authorization
handlers; runtime IDs are never browser commands.

## Consequences

Group quotas are easy to manage for large groups, but changing membership can
change effective admission limits immediately. The UI must show whether a user
has an explicit quota or inherits group quotas. Runtime inspection remains the
place for cross-user container operations.
