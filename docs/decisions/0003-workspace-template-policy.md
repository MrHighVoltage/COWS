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
- default storage allocation in bytes;
- approved access-method names: terminal, desktop, and web;
- allowed roles; and
- enabled state.

The browser presents memory and storage in MiB/GiB, but the backend converts
them to integer bytes before validation and storage. The service rejects empty
or malformed values, invalid digests, unsupported methods or roles, duplicate
policy entries, inverted resource ranges, and values above conservative bounds.

## Storage

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
