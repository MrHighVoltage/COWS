# COWS Security

COWS is not production-ready. This document records the intended boundaries and
known limitations; it is not a security certification.

## Threat model

Assume that an authenticated user may be malicious, may tamper with every
browser request, and may know identifiers belonging to another user. Assume
that a workspace may run untrusted software. Protect other users, the host,
the runtime socket, secrets, and approved internal services from that user.
Also account for administrators, reverse-proxy mistakes, runtime restarts,
partial operations, and compromised or misconfigured images.

## Trust boundaries

- The browser is untrusted. Client-side validation and hidden controls are not
  authorization.
- COWS HTTP handlers are the public application boundary.
- Domain services enforce ownership, role, template, quota, and state policy.
- The runtime adapter is the only boundary to Docker or Podman.
- Managed containers are isolated workloads, not trusted application code.
- A reverse proxy is part of the deployment security boundary and must preserve
  HTTPS, secure headers, and correct client identity handling.

## Required controls

- HTTPS is the only public access path.
- No direct public VNC, SSH, terminal, or workspace application ports.
- Every backend operation checks authentication and authorization independently.
- Browser requests never choose runtime targets, internal URLs, arbitrary ports,
  images, mounts, capabilities, or runtime arguments.
- Sessions use secure, HttpOnly, SameSite cookies where appropriate, with CSRF
  protection for state-changing requests.
- User-controlled values are escaped by `html/template` and are not inserted
  through unsafe client-side HTML APIs.
- Requests, uploads, proxy bodies, and upstream operations have size and time
  limits.
- Logs and audit records exclude passwords, tokens, terminal contents, file
  contents, and sensitive environment values.
- Login failures are rate-limited per source by the single COWS process. A
  multi-instance deployment must move this control to shared infrastructure.
- Newly created users must change their initial password before administrator
  operations are available. Email is stored for future notifications but is
  not yet used for recovery or delivery.
- Runtime capabilities are least privilege. Privileged mode, host networking,
  unrestricted host mounts, arbitrary capabilities, and direct devices are not
  user-selectable.
- COWS verifies runtime capability flags before creating a workspace. A
  Docker-compatible API is not proof that CPU, memory, process, or storage
  limits are enforced, especially for rootless Podman.
- Timeout policies are administrator-controlled and evaluated by the backend;
  users cannot extend them by changing browser data or suppressing timers.
- Container deletion and future data archival are separate actions. Archive
  eligibility must never be treated as authorization to read or delete data,
  and any future archive implementation must be separately audited.

## Access gateway risks

Terminal and desktop WebSockets must validate the authenticated user, workspace
ownership or administrator permission, workspace state, template permission,
and session expiry. The server selects the runtime target and handles terminal
resize; the browser cannot submit a container ID or address. noVNC must route
through COWS and must never expose a workspace VNC port.

The web-application proxy must be allowlisted by template and internal port.
It is not a generic URL proxy. SSRF, redirects, cookies, host headers, origins,
WebSocket upgrades, path rewriting, response sizes, and upstream timeouts all
need explicit policy and tests before that milestone ships.

## Runtime and host risks

Access to Docker or Podman can become host-level access. The initial single
process therefore owns that boundary. Deployments must protect the runtime
socket and COWS data directory, use a dedicated service account where
practical, and avoid exposing the service directly without HTTPS and a trusted
reverse-proxy configuration.

Resource limits must be enforced by Docker or Podman wherever possible. COWS
quotas alone are not a containment mechanism. Capacity calculations must fail
closed when host information is unavailable.

Administrator host settings control allocatable storage and reserved host
resources. They are persisted in SQLite, protected by administrator
authorization and CSRF checks, and audited. The startup configuration value
only initializes missing settings; it does not silently overwrite web-managed
values. A zero storage capacity intentionally fails workspace admission closed.

Lifecycle timeout processing must fail closed around ambiguous state. If COWS
cannot establish whether a workspace was connected, stopped, or deleted, it
must not perform an irreversible deletion or archival action. Future email
warnings must not include secrets, tokens, terminal contents, or sensitive
workspace data.

The current scheduler reserves allocations for stopped workspaces and requires
an explicit host-storage capacity. It does not support overcommit, GPU
capacity, or multiple active schedulers.

## Future file-manager risks

File access is not implemented. Before it is added, the backend needs approved
roots, canonical path handling, symlink and race protection, filename policy,
upload and temporary-storage limits, safe archive inspection, ZIP-slip and
ZIP-bomb defenses, and tests proving that a user cannot reach another
workspace, a runtime socket, a host path, or container secrets.

## Known limitations of the current milestone

Local username/password authentication, mandatory first-login password change,
stored email fields, server-side opaque sessions, CSRF protected forms,
administrator checks, login rate limiting, and basic audit persistence now
exist. The implementation has no OpenID Connect, password recovery, account
deletion workflow, terminal, desktop, proxy, file manager, or production HTTPS
configuration. Docker lifecycle operations are limited to approved images,
labels, resource limits, and isolated network policy; runtime reconciliation
and interactive access still need further review. Audit failures and
operational alerts still need a deliberate policy. Do not deploy it as a
service for untrusted users.

## Reporting vulnerabilities

Until a dedicated security contact is published, report suspected
vulnerabilities privately to the project maintainers rather than opening a
public issue with exploit details. Include the affected commit, reproduction
steps, impact, and any suggested mitigation. Do not include real credentials,
tokens, workspace data, or host secrets in a report.
