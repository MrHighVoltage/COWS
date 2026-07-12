# ADR 0007: Server-side template runtime configuration

Status: accepted for the template configuration milestone

## Decision

COWS stores administrator-controlled container configuration as a typed JSON
document attached to a workspace template. A workspace snapshots that document
and its template revision when created. Users submit only a workspace name and
approved template ID; they never submit or receive runtime configuration.

The supported configuration vocabulary initially contains:

- an administrator-selected command;
- environment variables with literal values or approved COWS placeholders;
- managed named-volume mounts with container-only paths;
- TCP or UDP services with administrator-defined host-port ranges.

The resolver accepts only these placeholders:

```text
{{cows.workspace_id}}
{{cows.workspace_name}}
{{cows.service.<name>.port}}
{{cows.mount.<name>.path}}
```

There is no general Go-template execution, shell expansion, host environment
lookup, or user-provided Docker argument map. The resolver validates every
field before producing a COWS runtime specification.

Port allocations are persisted in SQLite and protected by a unique protocol /
host-port constraint. Allocations are stable while a workspace exists and are
released when timeout cleanup deletes its container or when the workspace is
explicitly deleted. Service bindings are loopback-only host bindings for the
initial Docker-compatible adapter. A later access gateway may use those
bindings without making them public container ports.

## Consequences

Template edits do not silently change existing workspace configuration. The
workspace snapshot provides deterministic retries and a clear reconciliation
input. Template revision history beyond the workspace snapshot is deferred.

Runtime-specific fields remain in the Docker or Podman adapter. Dangerous
options such as privileged mode, host networking, arbitrary binds, devices,
and capabilities are not represented by this configuration model.

Sensitive environment values are stored as administrator configuration and are
never returned to users or written to audit metadata. A dedicated secret store
is still required before using this mechanism for high-value credentials.
