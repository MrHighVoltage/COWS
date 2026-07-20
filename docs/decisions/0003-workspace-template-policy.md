# ADR 0003: Workspace Template Policy Surface

Status: accepted for the template-management milestone

## Decision

Administrators define workspace templates. Users do not submit container image
references, runtime arguments, host paths, capabilities, devices, or network
settings. A template currently contains:

- display name and description;
- image reference and an optional validated `sha256` digest;
- default and maximum CPU in millicores;
- default and maximum memory in bytes;
- whether users may select CPU and memory between the default and maximum;
- approved access-method names: terminal, desktop, and web;
- allowed roles; and
- enabled state.

The browser presents memory in MiB, but the backend converts it to integer
bytes before validation and storage. Fixed templates always use their defaults;
selectable templates accept a browser request only within the stored range, and
the scheduler checks live user and host availability again during creation. The
service rejects empty
or malformed values, invalid digests, unsupported methods or roles, duplicate
policy entries, inverted resource ranges, and values above conservative bounds.

## Storage

Storage is measured after creation for display and user-level allowance
accounting. Templates and workspaces do not reserve or enforce a storage
allocation; runtime storage limits are not populated from this policy.

The initial SQLite schema stores access methods and allowed roles as JSON text
inside the template row. This is appropriate for the low-volume control plane
while the policy surface is small. If template access rules become query-heavy
or group-based, a normalized policy table can be introduced by migration.

## Security boundary

Template creation and updates require an active administrator in the service
layer, independently of the HTTP controls. The runtime adapter will receive a
validated server-generated specification later; it will never receive raw
browser form values. Dangerous runtime features are not representable in this
model and require a separate security review before any addition.
