# ADR 0016: Stopped-workspace file access and archive audit

## Status

Accepted for the initial rootless Podman file-manager implementation.

## Decision

Approved file-manager mounts may be accessed while a workspace is `running`,
`stopped`, or `exited`. Rootless Podman file access is storage-backed through
the COWS namespace helper and does not require a process inside the workspace
to be running. Access remains limited to directory and named-volume mounts
explicitly marked `file_manager` in the stored template configuration.

COWS serializes file operations with workspace lifecycle operations using a
per-workspace in-process lock. Start, stop, explicit delete, and timeout
cleanup cannot change the runtime or move managed directories while a file
operation is active. Streamed file and ZIP downloads retain the lock until the
returned stream closes.

Explicit workspace deletion archives the complete managed directory tree in
the configured sibling archive root. The archive root also contains the
append-only `archive-activity.jsonl` log. Each record includes a UTC timestamp,
action, workspace ID, runtime/container ID when known, source and archive
paths, status, and a bounded error description when applicable. It never
contains file contents, credentials, terminal data, or environment values.

Timeout cleanup may stop or remove a runtime container, but it must not archive
or delete user data. Retained named-volume tombstones remain outside the file
manager authorization path and are for later administrator recovery tooling.

## Consequences

Users can inspect and manage approved mounted data without starting a stopped
workspace. Administrators can trace explicit container removal and managed
directory archival even after the workspace database record has been deleted.
The single-process lock is intentionally local; a future multi-host agent must
move this coordination to the host boundary.
