# ADR 0011: Runtime-backed file access

## Status

Accepted for the initial rootless Podman file-manager implementation.

## Problem

COWS-managed directory mounts are prepared for rootless Podman subordinate
UID/GID mappings. The COWS host process owns the per-container parent, but the
inner mount directory is owned by the mapped container identity. A normal
host-side `os.Root` therefore cannot access it. Named volumes have the same
problem and must not be accessed by guessing paths inside container storage.

## Decision

File operations go through an optional runtime file-access capability. The
browser still supplies only a workspace ID, approved mount name, and relative
path. COWS resolves the workspace, template mount, runtime ID, container
identity, and storage source before opening the capability.

For rootless Podman on the initial single-host deployment, the Podman adapter
starts the same COWS executable as a short-lived file helper through:

```text
podman unshare cows file-helper ...
```

The helper:

1. receives a server-selected absolute source path and a validated operation;
2. enters the approved source directory while namespace-root so the COWS-owned
   parent path can be traversed;
3. drops to the mapped container UID/GID;
4. opens `.` with Go rooted filesystem operations; and
5. performs only the requested bounded operation.

The helper never receives a browser-selected host path, runtime ID, volume
name, command, or shell expression. Arguments are passed as argv values, not
through a shell. The helper rejects absolute paths, traversal, backslashes,
symlinks, special files, oversized transfers, and excessive ZIP entries.

Directory mounts use their server-created source path. Named volumes are
resolved through the Podman Libpod volume-inspect endpoint and the resulting
mountpoint is passed only to the namespace helper. The browser never sees the
mountpoint or volume name.

The runtime capability supports listing, stat/read, streamed ZIP generation,
directory creation, deletion, rename, and streamed file upload. ZIP uploads
remain unsupported. Read-only policy is enforced both by COWS before opening
the operation and by the helper for mutation operations.

## Why no helper container

A separate file container would need access to every approved bind mount and
volume, would introduce another container lifecycle, and would require another
user-namespace policy. The namespace helper keeps the operation in the same
rootless UID/GID mapping and works independently of tools installed in the
workspace image. A future host agent can replace the local process boundary
without changing browser routes or authorization rules.

## Boundaries and limitations

- The helper is a local runtime implementation detail, not a public service.
- The COWS process must run as the same account that owns the rootless Podman
  service and must have `podman` available in `PATH`.
- The initial Podman adapter uses the local helper path. Multi-host
  execution requires a future host-agent boundary.
- Non-rootless directory and named-volume mounts are outside the supported
  deployment and require a separately reviewed privilege boundary.
- File operations may use a running, stopped, or exited workspace. Terminal
  and desktop sessions still require a running workspace. Lifecycle changes
  are serialized with file operations, and explicit directory archival writes
  recovery identifiers to `archive-activity.jsonl`.
- File contents and terminal contents are not written to audit logs.

## Consequences

The file manager can use both administrator-approved directory mounts and
named volumes without weakening rootless ownership. The runtime interface
becomes responsible for storage access details, while the file service keeps
browser authorization, path validation, response limits, and UI behavior.
The helper protocol is intentionally small so it can later be replaced by a
privileged or per-host agent after multi-host requirements are real.
