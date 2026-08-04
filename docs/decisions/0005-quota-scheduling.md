# ADR 0005: Initial Quota and Capacity Scheduling

Status: accepted for the workspace preparation milestone

## Decision

COWS uses a deterministic admission check before recording a workspace:

1. Load the user and determine whether the active account is an administrator.
2. Load the user's assigned CPU, memory, storage, total-workspace, and
   running-workspace quota.
3. Sum measured storage for all existing workspaces; sum CPU and memory only
   for observed running workspaces; count total and running workspaces
   separately.
4. Reject the request if an ordinary user has no assigned quota, if an assigned
   finite storage allowance is already exhausted, or if a CPU, memory, or
   workspace-count limit would be exceeded. Storage is measured for the
   allowance check; no per-template or per-workspace storage allocation is
   reserved.
5. Load host CPU and memory capacity.
6. Scale physical host CPU and memory by their configured overbooking factors,
   then subtract all existing workspace allocations.
7. Reject the request if any requested resource does not fit.

The default CPU and memory factors are `1.0`, which does not overbook. Values
below `1.0` leave headroom; values above `1.0` allow admission overbooking and
are warned about in the administrator UI because memory overbooking can lock up
the host. Runtime hard limits still apply to each container. CPU and memory
allocations from stopped workspaces are not counted; storage usage and total
workspace count remain counted. A missing quota is an error for ordinary users,
while administrators are unlimited when no quota row is assigned. A zero value
in an assigned quota explicitly means unlimited for that resource. Unknown
host CPU or memory capacity remains an error for every user, including
administrators.

## Capacity sources

Rootless Podman reports host CPU and memory through its local service API.
The informational storage capacity, reserved storage value, and CPU/memory
overbooking factors are stored in the singleton `host_settings` record.
`COWS_HOST_STORAGE_BYTES`,
`COWS_HOST_CPU_OVERBOOKING_FACTOR`, and `COWS_HOST_MEMORY_OVERBOOKING_FACTOR`
seed the record when it is first created; administrators can update them through
the web UI without a process restart. A zero storage value means unknown and
does not block workspace creation.
Administrator updates create audit events.

Measured storage is the writable container layer plus helper-measured managed
mount and volume data, excluding the image. If measurement is unavailable,
finite storage allowance checks fail closed. The scheduler is intentionally
local and synchronous. It is not a distributed scheduler and does not
coordinate multiple active COWS instances. Its admission lock and account
throttles are process-local by design.
