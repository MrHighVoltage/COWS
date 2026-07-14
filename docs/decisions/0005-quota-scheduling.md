# ADR 0005: Initial Quota and Capacity Scheduling

Status: accepted for the workspace preparation milestone

## Decision

COWS uses a deterministic admission check before recording a workspace:

1. Load the user and determine whether the active account is an administrator.
2. Load the user's assigned CPU, memory, storage, total-workspace, and
   running-workspace quota.
3. Sum measured storage for all existing workspaces; sum CPU and memory only
   for running workspaces; count total and running workspaces separately.
4. Reject the request if an ordinary user has no assigned quota or if an
   assigned finite quota would be exceeded.
5. Load host CPU, memory, and storage capacity.
6. Subtract reserved capacity and all existing workspace allocations.
7. Reject the request if any requested resource does not fit.

There is no unsafe overcommit. Allocations remain reserved for stopped
workspaces under the initial policy. A missing quota is an error for ordinary
users, while administrators are unlimited when no quota row is assigned. A
zero value in an assigned quota explicitly means unlimited for that resource.
Unknown host capacity remains an error for every user, including
administrators.

## Capacity sources

Rootless Podman reports host CPU and memory through its local service API.
Storage capacity and reserved resources are stored in the singleton `host_settings`
record. `COWS_HOST_STORAGE_BYTES` seeds the record when it is first created;
administrators can update it and the reserved values through the web UI without
a process restart. A zero storage value means unknown and causes admission to
fail closed. Administrator updates create audit events.

Measured storage is the writable container layer plus helper-measured managed
mount and volume data, excluding the image. If measurement is unavailable,
workspace admission fails closed. The scheduler is intentionally local and synchronous. It is not a distributed
scheduler and does not coordinate multiple active COWS instances.
