# ADR 0005: Initial Quota and Capacity Scheduling

Status: accepted for the workspace preparation milestone

## Decision

COWS uses a deterministic admission check before recording a workspace:

1. Load the user's assigned CPU, memory, storage, and workspace-count quota.
2. Sum allocations for the user's existing workspace records.
3. Reject the request if user quota would be exceeded.
4. Load host CPU, memory, and storage capacity.
5. Subtract reserved capacity and all existing workspace allocations.
6. Reject the request if any requested resource does not fit.

There is no unsafe overcommit. Allocations remain reserved for stopped
workspaces under the initial policy. A missing quota or unknown host capacity is
an error, not permission to continue.

## Capacity sources

The Docker adapter reports host CPU and memory through Docker `/info`. Storage
capacity and reserved resources are stored in the singleton `host_settings`
record. `COWS_HOST_STORAGE_BYTES` seeds the record when it is first created;
administrators can update it and the reserved values through the web UI without
a process restart. A zero storage value means unknown and causes admission to
fail closed. Administrator updates create audit events.

The scheduler is intentionally local and synchronous. It is not a distributed
scheduler and does not coordinate multiple active COWS instances.
