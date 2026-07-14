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
- an optional typed container-user block with administrator-selected numeric
  UID/GID, shell, home, and display-name overrides.

The resolver accepts only these placeholders:

```text
{{cows.workspace_id}}
{{cows.workspace_name}}
{{cows.service.<name>.port}}
{{cows.mount.<name>.path}}
{{cows.user.username}}
{{cows.user.display_name}}
```

There is no general Go-template execution, shell expansion, host environment
lookup, or user-provided runtime argument map. The resolver validates every
field before producing a COWS runtime specification.

Port allocations are persisted in SQLite and protected by a unique protocol /
host-port constraint. Allocations are stable while a workspace exists and are
released when timeout cleanup deletes its container or when the workspace is
explicitly deleted. Service bindings are loopback-only host bindings for the
initial Podman adapter. A later access gateway may use those
bindings without making them public container ports.

## Consequences

Template edits do not silently change existing workspace configuration. The
workspace snapshot provides deterministic retries and a clear reconciliation
input. Template revision history beyond the workspace snapshot is deferred.

Runtime-specific fields remain in the Podman adapter. Dangerous
options such as privileged mode, host networking, arbitrary binds, devices,
and capabilities are not represented by this configuration model.

When `container_user` is present, COWS resolves the existing application
username for the workspace owner and builds a passwd entry in the form
`username:x:uid:gid:name:home:shell`. The template may override the username
only with `{{cows.user.username}}`; it may override the other fields with
literal values or the approved user placeholders. Podman receives the
controlled `uid:gid` selection and uses its Libpod create API so the
passwd entry is actually written into the container.

Sensitive environment values are stored as administrator configuration and are
never returned to users or written to audit metadata. A dedicated secret store
is still required before using this mechanism for high-value credentials.
