# ADR 0002: Runtime Adapter Boundary

Status: accepted as an interface-only preparation

## Decision

COWS communicates with the local rootless Podman service through
`internal/runtime.Runtime`.
The interface uses COWS concepts such as workspace IDs, validated image
references, resource limits, capabilities, and observed lifecycle state. Podman
API or client types must not cross this boundary.

The initial interface covers capability reporting, managed-workspace listing,
create/start/stop/remove lifecycle operations, and inspection. The selected
Podman adapter implements capability reporting, host capacity,
managed-workspace listing, and inspection; mutation returns `ErrNotSupported`.
Terminal attach, logs, and resource sampling remain separate design work; they
are not forced into this base interface. A separate optional
`InternalServiceRuntime` interface supports the dedicated, server-selected VNC
desktop gateway without turning the base runtime contract into a generic proxy.

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

- Podman SDK dependencies.
- Container creation, start/stop, or deletion.
- Runtime-specific resource-limit translation.
- Terminal, generic graphical desktop, proxy, and log-stream interfaces.
- Multi-host agents and remote transport.

The interface-only inspection coordinator now exercises the read-only portion
of this contract: it requests capabilities and managed workspaces, validates
identity uniqueness, and returns deterministic ordering without mutating the
runtime or database. The first adapter should be selected only after fake-runtime
service tests define the required operation semantics and error handling. The
adapter uses Podman's local compatibility API and Docker support is out of
scope.
