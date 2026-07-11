# ADR 0002: Runtime Adapter Boundary

Status: accepted as an interface-only preparation

## Decision

COWS will communicate with Docker and Podman through `internal/runtime.Runtime`.
The interface uses COWS concepts such as workspace IDs, validated image
references, resource limits, capabilities, and observed lifecycle state. Docker
or Podman SDK types must not cross this boundary.

The initial interface covers capability reporting, managed-workspace listing,
create/start/stop/remove lifecycle operations, and inspection. Terminal attach,
desktop forwarding, logs, and resource sampling remain separate design work;
they are not forced into this base interface.

## State and reconciliation semantics

`WorkspaceSpec` represents a server-generated desired specification. It is built
from an administrator-approved template and policy, never directly from a
browser request. `ObservedWorkspace` represents runtime truth at a point in
time. COWS will persist desired and observed state separately and periodically
call `ListManaged` and `InspectWorkspace` to reconcile them.

Lifecycle methods should be idempotent where the runtime permits it. Adapters
must map runtime-specific failures to stable COWS-facing categories such as
not found, conflict, unavailable, and not supported. The base interface does
not prescribe unsafe fallback behavior.

Managed runtime objects are identified with COWS labels, including the COWS
workspace ID. Browser clients never receive authority to select runtime IDs or
targets; those values remain service-side data.

## Deliberately deferred

- Docker or Podman client libraries and socket access.
- Container creation, start/stop, inspection, or deletion.
- Runtime-specific resource-limit translation.
- Terminal, graphical desktop, proxy, and log-stream interfaces.
- Multi-host agents and remote transport.

The first adapter should be selected after fake-runtime service tests define
the required operation semantics and error handling.
