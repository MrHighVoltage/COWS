# ADR 0028: Minimum Podman Version

## Status

Accepted

## Context

The Podman adapter creates workspaces with a configured `container_user`
through the Libpod container-create endpoint and sends the resolved passwd line
in `passwd_entry` (ADR 0009). Podman decodes that request without rejecting
unknown fields. `passwd_entry` first exists in Podman 4.1.0, so an older
Podman, such as the 3.4.4 package shipped by Ubuntu 22.04, drops it without an
error and writes its own fallback line instead:

```text
1000:*:1000:1000:container user:<working directory>:/bin/sh
```

The workspace then starts with the UID as its username, and nothing reports
the problem to an administrator.

## Decision

COWS requires rootless Podman 4.1.0 or newer. The adapter reads the Podman
release from `ServerVersion` in the same `/info` response it already uses to
confirm rootless mode, and checks it in both `Name` and `Capabilities`. An
older release, or a response without a recognizable `major.minor` version,
fails closed with `runtime.ErrNotSupported`. The error names the required
minimum and the major and minor version found, never other runtime detail.
Pre-release builds of 4.1.0 and later, such as `4.1.0-dev`, are accepted.

The check is global rather than limited to templates with a `container_user`:
COWS is developed and tested only against releases that accept its full
create request, and a partially working runtime would hide the next silently
ignored field in the same way.

## Consequences

- `GET /readyz` reports the runtime as unavailable on an unsupported Podman,
  because it calls `Name` (ADR 0024).
- Reconciliation and workspace creation fail closed, because they call
  `Capabilities`.
- A deployment on an older distribution must upgrade Podman before COWS can
  manage workspaces. Existing containers are left untouched.
- The version floor does not prove that Podman honors every field. Changes to
  the Libpod request still need verification against a real Podman.
- Raising the minimum updates this record, the adapter constants, and the
  rootless Podman requirements in `docs/configuration.md` together.
