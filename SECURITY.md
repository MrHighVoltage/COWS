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
- The runtime adapter is the only boundary to the local rootless Podman service.
- Managed containers are untrusted workloads. Templates without internal
  services use `none` networking; desktop-enabled templates use loopback-only
  host mappings. Optional isolation gives new service-enabled workspaces
  separate internal Podman networks and fails closed if creation fails. It is
  not a complete host-level egress policy and does not migrate existing
  containers.
- A reverse proxy is part of the deployment security boundary and must preserve
  HTTPS, secure headers, and correct client identity handling.

## Required controls

- HTTPS is the only public access path.
- No direct public VNC, SSH, terminal, or workspace application ports.
- Every backend operation checks authentication and authorization independently.
- Browser requests never choose runtime targets, internal URLs, arbitrary ports,
  images, mounts, capabilities, or runtime arguments.
- Template configuration is administrator-controlled, typed, and resolved on
  the server. Users cannot edit template JSON, placeholders, ports, mounts, or
  runtime parameters.
- Sessions use secure, HttpOnly, SameSite cookies where appropriate, with CSRF
  protection for state-changing requests.
- User-controlled values are escaped by `html/template` and are not inserted
  through unsafe client-side HTML APIs.
- Requests, uploads, proxy bodies, and upstream operations have size and time
  limits.
- Logs and audit records exclude passwords, tokens, terminal contents, file
  contents, and sensitive environment values.
- File-manager directory listings, individual downloads, uploads, and archive
  generation are bounded. Successful file mutations and downloads create
  audit events containing only the workspace, mount, path, and operation.
- User-facing state contains stable COWS error categories only. Detailed Podman,
  filesystem, and database errors remain administrator-side diagnostic data and
  must not be rendered to workspace owners.
- Login failures are rate-limited per source by the single COWS process. A
  multi-instance deployment must move this control to shared infrastructure.
- Administrator-created users must change their initial password before
  administrator operations are available. Self-registered users choose their
  password during registration. Optional local password reset uses a hashed,
  single-use token and an HTTPS external URL; email is not an identity proof
  or authorization factor.
- Runtime capabilities are least privilege. Privileged mode, host networking,
  unrestricted host mounts, arbitrary capabilities, and direct devices are not
  user-selectable.
- COWS verifies runtime capability flags before creating a workspace. A
  Podman compatibility API is not proof that CPU, memory, process, or storage
  limits are enforced; COWS checks the reported rootless capabilities.
- Timeout policies are administrator-controlled and evaluated by the backend;
  users cannot extend them by changing browser data or suppressing timers.
- Container deletion and future data archival are separate actions. Archive
  eligibility must never be treated as authorization to read or delete data,
  and any future archive implementation must be separately audited.
- Runtime objects without a matching COWS workspace are treated as orphaned
  observations and are not automatically removed by reconciliation.
- Template placeholders cannot access host environment variables, host paths,
  secrets, or arbitrary runtime objects. Port bindings are loopback-only.
  Managed volume and directory mounts use engine-generated names derived from
  the workspace ID and administrator-controlled prefix/suffix values; users
  cannot choose or edit them. Directory or named-volume mounts explicitly
  marked for the file manager are exposed through rooted runtime operations.
- The optional template `container_user` block is administrator-controlled. It
  is resolved from the server-side COWS username and validated UID/GID, home,
  shell, and display-name fields. Rootless Podman uses explicit UID/GID
  mappings derived from its subordinate-ID map. The COWS host account is
  intentionally not mapped into the container; container-owned mount contents
  therefore use subordinate host IDs. The per-container parent remains
  COWS-owned for lifecycle moves. Rootless Podman resolves the passwd-entry
  extension server-side; users never select it.
- A template may explicitly allow terminal sessions for selected container UIDs
  through `terminal_uids`. The browser may request one of those values, but the
  backend revalidates the allowlist before the Podman exec request. Allowing UID
  0 grants root inside that workspace, including control of writable mounts and
  container-visible secrets; it does not grant host root under correct rootless
  subordinate-ID mappings. It remains high risk if templates expose host paths,
  runtime sockets, devices, unsafe capabilities, or vulnerable workloads.

## Access gateway risks

Terminal and desktop WebSockets validate the authenticated user, workspace
ownership or administrator permission, workspace state, template permission,
and session expiry. The server selects the runtime target and internal service
port; the browser cannot submit a container ID, address, or port. The desktop
gateway accepts only the template's `desktop` TCP service, verifies its
loopback-only runtime mapping, and bridges raw VNC traffic through COWS. noVNC
must never expose a workspace VNC port. COWS keeps VNC authentication enabled,
uses the template-selected per-workspace password, and returns it only to an
already authorized desktop session with `Cache-Control: no-store`. Generated
secrets are resolved separately for each workspace; static template secrets
are an administrator responsibility. Secrets are not rendered in ordinary
pages, URLs, logs, or audit events. The current
control-plane database stores this runtime secret and therefore requires the
existing restrictive database-file permissions.

The web-application proxy must be allowlisted by template and internal port.
It is not a generic URL proxy. SSRF, redirects, cookies, host headers, origins,
WebSocket upgrades, path rewriting, response sizes, and upstream timeouts all
need explicit policy and tests before that milestone ships.

## Runtime and host risks

Access to rootless Podman can still become host-level access. The initial single
process therefore owns that boundary. Deployments must protect the runtime
socket and COWS data directory, use a dedicated service account where
practical, and avoid exposing the service directly without HTTPS and a trusted
reverse-proxy configuration.

Resource limits must be enforced by rootless Podman wherever possible. COWS
quotas alone are not a containment mechanism. Capacity calculations must fail
closed when host information is unavailable.

Administrator host settings control separate CPU and memory overbooking factors
and retain storage reporting settings for future policy work. A factor above
`1.0` is an explicit capacity-admission overbooking choice and can cause system
lockups, especially when memory is overbooked; it is not a runtime safety limit.
Settings are persisted in SQLite, protected by administrator authorization and
CSRF checks, and audited. The startup configuration values only initialize
missing settings; they do not silently overwrite web-managed values. Storage is
measured for display and finite user-allowance checks, not reserved per template
or workspace.

Lifecycle timeout processing must fail closed around ambiguous state. If COWS
cannot establish whether a workspace was connected, stopped, or deleted, it
must not perform an irreversible deletion or archival action. Future email
warnings must not include secrets, tokens, terminal contents, or sensitive
workspace data.

The current scheduler counts CPU and memory only for observed running
workspaces and checks selected resources against live host capacity after the
configured CPU and memory overbooking factors. It does not support GPU
capacity, host-level storage admission, or multiple active schedulers.

## File-manager risks

The initial file manager is intentionally narrow. It exposes only
administrator-approved directory or named-volume mounts marked `file_manager`,
never arbitrary container paths, runtime sockets, or host paths. The backend
uses server-side workspace authorization, relative-path validation, the runtime
file-access capability, rooted filesystem operations, safe filenames, read-only
checks, bounded uploads, temporary files, and server-side mount selection.
Rootless Podman operations run through the COWS namespace helper; COWS does not
weaken ownership mappings to make direct host access work. These controls
reduce but do not eliminate risk from a malicious workspace process, filesystem
races, or host misconfiguration. The helper may access `running`, `stopped`, or
`exited` workspaces because it operates on storage rather than executing inside
the container. File operations are serialized with lifecycle changes.
Directory downloads are streamed as ZIP archives with a 4 GiB uncompressed
and 100,000-entry bound. Symlinks and non-regular special files are not
included, and no temporary archive is created. Archive extraction, bulk
operations, and stronger quota accounting are not implemented. Before those
features are added, include explicit ZIP-slip, ZIP-bomb, symlink replacement,
file-count, temporary-storage, and generated-download-size tests.

Explicit deletion retains named volumes and records tombstone metadata before
the workspace row is removed. A retained-volume record is not authorization to
mount or restore that volume. Administrators can use the separate recovery view
to download or remove a validated tombstone, with audit events.

Explicit deletion also writes an append-only, permission-restricted
`archive-activity.jsonl` record containing workspace and runtime/container
identifiers, source/archive paths, timestamps, and operation status. It never
contains file contents or secrets and is intended to help administrators locate
data for manual recovery. Timeout cleanup never archives user data.

## Registration and email risks

Disabling a user invalidates all of that user's sessions in the same database
transaction as the disabled flag, before runtime stop operations begin. A
runtime outage can therefore prevent container cleanup but cannot leave the
account enabled or its existing sessions usable. User deletion requires a
previously disabled account and succeeds only after all workspaces have been
stopped and explicitly deleted through the normal archive and named-volume
tombstone path. Cleanup failures leave the disabled account and remaining
workspace records for an administrator retry.

Group removal does not delete existing workspaces. It can reduce template
access and effective quotas, so over-quota existing allocations are retained
but new allocations are denied until usage falls below the new limit. Group
deletion is blocked while a template references that group, preventing stale
policy references from being interpreted in a fail-open way.

Self-registration is disabled by default. Enabling it permits unauthenticated
account creation and therefore requires conservative default quotas, rate
limiting, monitoring, and an operator decision that email addresses do not
need to be verified yet. The server assigns the user role, default quota, and
  default groups; browser fields cannot select them. Registration does not
  provide email verification. Local password reset is a separate,
  non-enumerating, single-use-token flow and requires an HTTPS external URL.

SMTP credentials must be protected as deployment secrets. Email warnings are
advisory and may be delayed, duplicated around process crashes, rejected by a
relay, or unavailable. They never authorize access and never determine whether
COWS stops or deletes a container. Notification messages exclude secrets,
terminal contents, runtime identifiers, host paths, and internal addresses.

## Known limitations of the current milestone

Local username/password authentication, mandatory first-login password change,
stored email fields, server-side opaque sessions, CSRF protected forms,
administrator checks, login rate limiting, and basic audit persistence now
exist. Account disablement, safe user deletion, and group lifecycle controls
are implemented. The implementation has no generic application proxy or
institutional authentication. Caddy, nginx, and Apache HTTPS reverse-proxy examples are
prepared but are not enabled by the development process. The initial file manager supports approved
directory and named-volume listing, bounded upload,
individual file download, streamed directory ZIP download, folder creation,
rename, and deletion; it does not support archive extraction.
Administrator CSV imports are bounded, previewed before commit, restricted to
local user identity fields, and committed transactionally. Generated temporary
passwords are returned only through a short-lived administrator-bound download;
they are not stored in the database, logs, or audit metadata. Treat the export
as sensitive and delete it after distribution.
Terminal access uses a fixed server-resolved template shell, optionally with a
template allowlist of container UIDs, and desktop access uses a template-approved VNC service through the rootless Podman adapter; desktop sessions
automatically authenticate using the template-selected VNC secret when one is
configured. Both require runtime support for the selected container. Podman lifecycle
operations are limited to approved images, labels, and runtime-enforced
resource limits. Historical metrics, retained-volume restore, stronger
host-level egress policy, and deeper runtime integration still need review. Do
not deploy it as a service for untrusted users.

## Reporting vulnerabilities

Until a dedicated security contact is published, report suspected
vulnerabilities privately to the project maintainers rather than opening a
public issue with exploit details. Include the affected commit, reproduction
steps, impact, and any suggested mitigation. Do not include real credentials,
tokens, workspace data, or host secrets in a report.
