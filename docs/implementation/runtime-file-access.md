# Runtime File Access Implementation

This document describes the first implementation of browser file access for
rootless Podman. It is an implementation companion to
[ADR 0011](../decisions/0011-runtime-file-access.md), not a public API
contract.

## Request flow

1. The browser sends only the workspace ID, approved mount name, relative path,
   and operation-specific names.
2. `internal/files` asks the workspace service to authorize the workspace and
   return the currently available file mounts.
3. The workspace service resolves the stored template mount, runtime ID,
   container UID/GID, read-only policy, and server-managed source.
4. The Podman adapter opens a `runtime.FileAccess` capability from
   that server-selected specification.
5. The adapter starts the same COWS executable with `podman unshare` for each
   operation. The helper enters the approved source directory before dropping
   to the configured container identity and opens a rooted filesystem at `.`.
6. Listings and metadata use bounded JSON. File downloads, ZIP downloads, and
   uploads use pipes so COWS does not buffer the transfer or create a temporary
   archive.

## Supported operations

The initial capability supports listing, metadata lookup, regular-file
download, directory ZIP download, directory creation, deletion, rename, and
bounded regular-file upload. ZIP uploads and archive extraction are not
implemented.

The helper rejects absolute paths, traversal, backslashes, control
characters, symlinks, special files, duplicate destination names, uploads
over 128 MiB, ZIP archives over 4 GiB uncompressed, and ZIPs with over 100,000
entries. The service applies the same relative-path, mount, read-only, and
authorization checks before invoking the capability.

## Directory and volume sources

Directory mounts use the per-workspace source below `COWS_MOUNT_ROOT`. Named
volumes use the engine-generated volume name. The Podman adapter resolves the
volume mountpoint through its Libpod volume-inspect endpoint; the mountpoint is
never sent by the browser and never exposed in a route.

The initial helper requires the COWS process to run as the same account that
owns the rootless Podman service and requires `podman` in `PATH`. This is a
single-host implementation. A future host agent may own this capability for
multi-host deployments without changing the browser-facing file service.
Non-rootless directory mounts are outside the supported deployment and require
fallback; rootful named-volume access remains disabled until its privilege
boundary is designed.

## Verification

Focused tests cover rooted helper operations, traversal and symlink rejection,
runtime-backed service routing, and the existing local rooted file service.
The ordinary unit suite does not require Podman. A live rootless
verification should list and download an approved mount while checking that
the helper process exits after each operation.
