# ADR 0023: Optional Per-Workspace Network Isolation

## Status

Accepted

Network isolation is disabled by default for compatibility. When enabled, each
newly created desktop-enabled workspace receives a server-generated internal
Podman network derived from its opaque workspace ID. No public container port is
created. Workspaces without the approved desktop service continue to use Podman
`none` networking.

Network creation fails closed when Podman cannot create the private network.
Existing workspaces are not migrated when the setting changes; they must be
recreated. This isolates COWS-managed workspaces from one another but is not a
claim about host compromise or complete egress policy.
