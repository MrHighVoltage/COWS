# ADR 0010: Managed mounts and the initial file manager

## Status

Accepted for the initial restricted file-manager implementation.

## Decision

COWS supports two administrator-controlled template mount types:

- `volume`: a runtime-managed named volume.
- `directory`: a host directory below `COWS_MOUNT_ROOT`, mounted into the
  container as a bind mount.

The active layout is:

```text
COWS_MOUNT_ROOT/
  cows-<workspace-id>/
    <prefix><mount-name><suffix>/
COWS_MOUNT_ARCHIVE_ROOT/
  cows-<workspace-id>/
```

The per-container directory name is derived from the immutable COWS workspace
identifier because the runtime's opaque ID does not exist before bind sources
are passed to container creation. Prefixes and suffixes are validated, and
users never receive control over the resulting source, host path, or runtime
arguments. The per-container parent remains owned by COWS; rootless Podman
prepares inner bind directories for the explicit subordinate UID/GID mapping.
Explicit workspace deletion moves the complete per-container directory to the
sibling archive root. Both roots must be on the same filesystem for the
atomic move. Named volumes remain owned and removed by the runtime lifecycle.
Timeout cleanup currently keeps data for later retention handling.

Templates may set `file_manager: true` on directory or volume mounts. They
must also include the `files` access method. The browser file manager resolves
mounts from the authorized workspace service and uses the runtime file-access
capability with rooted filesystem operations; it accepts only relative paths
and safe entry names. Rootless Podman operations run through the namespace
helper documented in ADR 0011. Named volumes are exposed only through this
server-selected capability; their storage paths never reach the browser.

Directory downloads are generated as bounded ZIP streams. COWS does not buffer
the archive in memory or write a temporary archive, rejects symlinks, and does
not extract archives. The current limits are 4 GiB of uncompressed file data
and 100,000 archive entries.

## Rationale

This keeps runtime configuration typed and auditable, gives administrators
predictable per-workspace storage locations, and prevents browser input from
becoming a host path or volume selector. It also leaves a clear boundary for a
future privileged host agent and for stronger file auditing and archive policy.

## Consequences

Directory mounts require writable local COWS mount and archive roots and
inherit host filesystem availability. Explicit workspace deletion moves the
per-container directory before deleting the workspace record. The stable
container directory name and all inner mount names are preserved. Timeout
deletion does not archive data; it only marks later retention eligibility. The
initial file manager does not provide archive extraction, bulk operations, or
file previews. Those features require additional limits and security tests.
