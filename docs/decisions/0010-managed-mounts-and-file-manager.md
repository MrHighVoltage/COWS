# ADR 0010: Managed mounts and the initial file manager

## Status

Accepted for the initial restricted file-manager implementation.

## Decision

COWS supports two administrator-controlled template mount types:

- `volume`: a runtime-managed named volume.
- `directory`: a host directory below `COWS_MOUNT_ROOT`, mounted into the
  container as a bind mount.

The runtime source name is generated as
`cows-<workspace-id>-<prefix><mount-name><suffix>`. Prefixes and suffixes are
validated, and users never receive control over the resulting source, host
path, or runtime arguments. Directory roots are created with restrictive
permissions before container creation. Explicit workspace deletion removes
COWS-managed directory roots; named volumes remain owned and removed by the
runtime lifecycle. Timeout cleanup currently keeps data for later retention
handling.

Templates may set `file_manager: true` only on directory mounts. They must also
include the `files` access method. The browser file manager resolves mounts
from the authorized workspace service and uses rooted filesystem operations;
it accepts only relative paths and safe entry names. Named volumes are not
exposed to the browser in this milestone because a safe, user-facing volume
policy needs a separate design.

## Rationale

This keeps runtime configuration typed and auditable, gives administrators
predictable per-workspace storage locations, and prevents browser input from
becoming a host path or volume selector. It also leaves a clear boundary for a
future privileged host agent and for stronger file auditing and archive policy.

## Consequences

Directory mounts require a writable local COWS mount root and inherit host
filesystem availability. The initial file manager does not provide archives,
bulk operations, file previews, or named-volume access. Those features require
additional limits and security tests before implementation.
