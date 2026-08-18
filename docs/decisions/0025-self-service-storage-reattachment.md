# ADR 0025: Self-Service Storage Reattachment

## Status

Accepted. Supersedes ADR 0022's "Restore and reattachment are intentionally
deferred" for the specific case of a user reattaching their own retained
storage; ADR 0022's administrator recovery workflow (download/remove) is
unchanged and remains separate.

## Decision

A user may reattach their own retained named-volume or archived-directory
storage — left behind by explicitly deleting a workspace — onto a new
workspace, instead of starting with empty storage. This is user
self-service only; there is no administrator bypass on this path, and it
uses its own route namespace (`/storage/*`) and handlers, never the
administrator recovery routes.

Retained volumes already carried `owner_user_id`; no user-scoped list query
needed a schema change. Retained directories previously had no database
record at all — archiving only moved files on disk and logged to
`archive-activity.jsonl`, with no owner attribution — so a new
`retained_workspace_directories` table was added, one row per deleted
workspace (directory archiving already moves one whole per-container tree,
not per mount), storing a JSON snapshot of the archived directory-type
mounts' name/container-path/read-only/prefix/suffix.

**Ownership.** Every read, download, delete, and reattachment claim goes
through a store method that filters by `owner_user_id` in the query itself,
never a check performed after an unscoped lookup. A tombstone that does not
exist and one that exists but belongs to someone else return the identical
`ErrNotFound`/`ErrRetainedStorageIncompatible` outcome, so a probing request
cannot distinguish the two.

**Single use.** Reattaching consumes (deletes) the tombstone in the same
store call that looks it up (`ConsumeRetainedWorkspaceVolume` /
`ConsumeRetainedWorkspaceDirectory`), scoped by owner. A concurrent second
attempt on the same tombstone finds it already gone and fails cleanly rather
than racing. Cloning the same retained item onto multiple new workspaces is
not supported.

**Compatibility.** A retained item may only be reattached into a mount with
the same logical `Name` and the same type (volume↔volume,
directory↔directory) on the newly chosen template — validated before any
tombstone is consumed. Directory tombstones are all-or-nothing: every mount
recorded in the tombstone must have a same-named match on the new template,
or the whole reattachment is rejected.

**Volumes.** No new runtime capability was needed: Podman's own
container-create semantics already attach an existing named volume by name
if one exists, so reattachment only needed `materializeMount` to accept a
caller-supplied volume name instead of computing the workspace's usual
deterministic one. Because every COWS container on a host gets the same
static rootless ID mapping (`internal/runtime/podman`,
`explicitRootlessMapping`), the only thing that can make a reattached
volume's content unreadable in a new workspace is a different
`container_user` UID/GID between the old and new template. `RemapOwnership`
(Podman's `"U"` mount option) is now unconditional for volume mounts, not
just directory mounts as before, so ownership is corrected to the new
container's identity on first start regardless of which template originally
created the volume. A freshly created empty volume has nothing to chown, so
this costs nothing in the ordinary case.

Known limitation, not silently promised away: the `"U"` remap corrects
ownership, not arbitrary restrictive permission bits left by an unusual
prior workload (e.g. a file mode that still excludes the new UID after the
chown). This has not been a problem in testing but is not proven impossible.

**Directories.** Restoring is a plain host-side rename of each matched
archived mount subdirectory into the new workspace's freshly created (and
empty) mount directory — no runtime involvement, mirroring the existing
archive-on-delete move in spirit (`archiveMountDirectories`). Ownership
self-heals automatically because directory mounts already always set
`RemapOwnership`. The destination directory is removed immediately before
the rename rather than relying on `rename(2)`'s POSIX allowance to replace
an empty directory in place, since that is not honored consistently by
every filesystem this runs on.

**Consequence of the consumption ordering.** A volume tombstone is consumed
(deleted from the database) before the corresponding runtime step runs. If
a later step fails, the new workspace is rolled back but the tombstone is
not restored — the underlying named volume is never deleted by this path,
only the database record that made it discoverable, so data is not lost,
but the self-service "browse and reattach" trail is. True cross-system
atomicity (a database transaction spanning a Podman API call) is not
achievable, and this asymmetry was chosen over a more complex
compensating-transaction scheme as an accepted, documented trade-off rather
than a hidden gap.

A directory tombstone cannot take the same shortcut, because restoring one
is not a discoverability-only operation like deleting a database row — it
physically renames the archived files onto disk. An early failed fix
consumed and restored a directory tombstone at the same point in
`CreateWorkspace` as a volume tombstone, before the workspace row, its
ports, and (when combined with a volume reattachment) its container were
confirmed; a later failure in any of those steps then triggered the same
mount-directory cleanup used for an ordinary failed creation, which deleted
the just-restored files along with the fresh, empty ones — turning
"discoverability lost" into real data loss and contradicting the paragraph
above. `CreateWorkspace` therefore claims and restores a directory
tombstone last, after the workspace row and its ports are secured, and the
cleanup path never deletes mount directories once a restore has succeeded
(if a container start afterward still fails, the restored directory is
orphaned on disk rather than deleted).

**No admission deadlock.** `CreateWorkspace` already holds the service's
process-wide admission mutex (ADR 0012/0013's single-instance admission
lock) for its entire duration. Reattachment eagerly creates and starts the
runtime container from inside `CreateWorkspace` (by calling the same
internal logic `StartWorkspace` uses), so that logic no longer acquires
admission itself — only the public `StartWorkspace` entry point does. The
mutex was never reentrant; this was found and fixed as a real deadlock
during implementation, not a theoretical concern.
