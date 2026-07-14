# ADR 0012: Rootless Podman resources and group-based template access

## Status

Accepted for the next implementation phase.

## Runtime boundary

COWS supports rootless Podman only. The configured socket must identify a
rootless Podman service; a rootful service or a Docker daemon is rejected during
runtime capability inspection and workspace admission. The adapter may use
Podman's Docker-compatible API endpoints where Podman exposes them, but this
does not constitute Docker support.

The ordinary test and integration workflow targets a rootless Podman socket.
Docker-specific tests, configuration names, and deployment instructions are
removed or replaced with Podman equivalents.

## Timeout policy

COWS keeps the initial-connection timeout and stopped-container retention
timeout. Stopped-container cleanup may remove a container but never deletes or
archives its user data automatically. The post-deletion data-retention timeout,
archive-eligibility timestamp, configuration fields, and UI controls are
removed. Explicit user or administrator deletion retains its existing managed
directory archive behavior.

## Resource accounting

Storage is measured, not inferred from declared template allocations. For each
workspace, the runtime storage provider reports:

- the container writable layer size, excluding the image;
- the usage of every managed directory mount; and
- the usage of every managed named volume.

Mount usage is calculated through the approved runtime file-access helper, so
rootless subordinate-ID ownership is preserved. Storage usage is counted for
all workspaces, whether their containers are running or stopped. If storage
measurement is unavailable, quota admission fails closed.

CPU and memory allocations are counted only for workspaces whose observed
runtime state is `running`. Workspace counts are separate:

- total workspace count includes stopped, running, and not-yet-created records;
- running workspace count includes only observed `running` containers.

Zero means unlimited for each quota. Before creating a workspace, COWS checks
measured existing storage plus the template's default storage budget. This
conservative admission budget prevents a new empty workspace from bypassing
the storage limit before its first usage sample.

## Groups and template access

Users may belong to multiple administrator-managed groups. A template keeps
its existing role restriction and adds a group access mode:

- `include`: the user must belong to at least one selected group;
- `exclude`: membership in any selected group denies access; and
- no selected groups means include matches nobody and exclude matches everyone.

Group membership and template group rules are evaluated by the backend for
template listing and workspace creation. Browser controls are not authorization.

## Administrator runtime overview

The runtime overview joins managed runtime observations with COWS workspace
records. It shows workspace owner and template names, and exposes the same
authorized lifecycle and access links as the workspace overview. Every control
reuses the ordinary workspace authorization and CSRF-protected handlers; the
runtime page does not accept runtime IDs as browser commands.

## Consequences

The resource model becomes dependent on live runtime and helper measurements,
which adds latency and requires clear unavailable states. It avoids treating
SQLite allocation columns as physical disk usage. Group rules are explicit
without introducing a general policy engine. A future host agent must preserve
the storage-provider and authorization interfaces.
