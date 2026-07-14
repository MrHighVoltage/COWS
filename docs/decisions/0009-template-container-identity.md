# ADR 0009: Template-controlled container identity

Status: accepted

## Decision

Workspace templates may contain an optional typed `container_user` object. Its
presence enables a COWS-generated passwd entry and process identity. The
administrator must provide numeric `uid` and `gid` values. `username` defaults
to the owning COWS user's existing `username` and may only be explicitly set
to `{{cows.user.username}}`. `name`, `home`, and `shell` have safe defaults and
may be overridden by validated template values or approved user placeholders.

COWS resolves this configuration when it produces a runtime specification:

```text
username:x:uid:gid:name:home:shell
```

The browser never supplies these values and users cannot edit them per
workspace. The workspace retains the administrator template snapshot, while
the owner username is loaded server-side from the user record.

## Runtime handling

The Docker-compatible API receives the controlled `uid:gid` process user. It
cannot represent Podman's passwd-entry extension, so a Docker runtime rejects
a specification that requires that extension instead of silently ignoring it.
For Podman, the adapter uses the Libpod container-create endpoint and sends
the typed `user`, `passwd_entry`, resource, mount, network, and port fields.

This is intentionally not a general runtime-argument escape hatch. Privileged
mode, host networking, arbitrary capabilities, host binds, device access,
user namespaces, and group-entry editing remain outside the template surface.

## Consequences

Images that already contain the selected UID/GID can use the process identity
without a passwd-entry block. Images that need a named login identity can opt
into the block, particularly for rootless Podman workspaces. Existing users do
not need a schema migration because COWS already stores a normalized username;
existing workspaces keep their template snapshot and are unaffected until
recreated.
