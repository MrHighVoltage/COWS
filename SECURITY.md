# COWS Security

COWS is not production-ready. This document records the intended boundaries and
known limitations; it is not a security certification.

## Threat model

Assume that an authenticated user may be malicious, may tamper with every
browser request, and may know identifiers belonging to another user. Assume
that a workspace may run untrusted software. Protect other users, the host,
the runtime socket, and secrets from that user.
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
  host mappings. Optional isolation gives new desktop-enabled workspaces
  separate internal Podman networks and fails closed if creation fails. It is
  not a complete host-level egress policy and does not migrate existing
  containers.
- A reverse proxy is part of the deployment security boundary and must preserve
  HTTPS, secure headers, and correct client identity handling.

## Required controls

- HTTPS is the only public access path.
- No direct public VNC, SSH, terminal, or workspace application ports.
- Production operation requires a tested backup and restore procedure for the
  SQLite control plane and managed data roots. Retained named volumes need a
  separate runtime-aware recovery or export procedure.
- Every backend operation checks authentication and authorization independently.
- Browser requests never choose runtime targets, internal URLs, arbitrary ports,
  images, mounts, capabilities, or runtime arguments.
- Template configuration is administrator-controlled, typed, and resolved on
  the server. Users cannot edit template JSON, placeholders, ports, mounts, or
  runtime parameters.
- Sessions use secure, HttpOnly, SameSite cookies where appropriate. CSRF cookies
  are signed with a process-local key and bound to the session; mutations require
  the matching form token and reject cross-origin browser metadata. Signing in
  or restarting requires old forms to be reloaded. HTTPS external URLs require
  Secure cookies; public credential email links require HTTPS.
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
- Form validation messages are the one exception, and only because they never
  come from the runtime, the filesystem, or the database: each is written by the
  rule it reports and describes the value the submitter just typed. The
  template editor's container-configuration field additionally relays the JSON
  decoder's position or key, which is still a reading of the administrator's own
  submitted text. Do not widen this to relay an error from any other source.
- The template configuration form editor and the JSON editor accept the same
  typed vocabulary and pass through the same server-side validation. The
  editor endpoint behind switching modes and adding rows only converts the
  submitted configuration and stores nothing. Both editors show stored secret
  and environment values to administrators, as the JSON editor always has.
- Login attempts reserve source allowance atomically before password work;
  concurrent in-flight attempts count toward the same cap. Registration and
  reset/invitation confirmation count successes too. These controls remain local
  to one process and use the direct peer address; trusted-proxy identity and
  per-account login policy remain unresolved design work.
- `/healthz` is a liveness and database-connectivity check only. It does not
  establish rootless-Podman readiness. `/readyz` checks both dependencies
  under a bounded timeout and is unauthenticated like `/healthz`, revealing
  only constant status words, never runtime or filesystem detail; use it, not
  `/healthz`, for supervisor or reverse-proxy traffic admission.
- Administrator-created users must change their initial password before
  administrator operations are available. Self-registered users choose their
  password during registration. Optional local password reset uses a hashed,
  single-use token and an HTTPS external URL; email is not an identity proof
  or authorization factor.
- An account created without a password stores a bcrypt hash of a random value
  that is immediately discarded, never an empty or sentinel hash, so nothing
  can authenticate against it. Such an account is opened only through a mailed
  single-use invitation. Invitation and reset tokens share storage and their
  security properties but carry an explicit purpose, and neither path accepts
  the other's token. Replacing a token is scoped to its own purpose and cancels
  any queued message carrying the superseded link.
- An email address belongs to at most one account. The unauthenticated reset
  endpoint resolves an identifier to a single account, so two accounts sharing
  an address would leave it undecided which one a mailed link opens; account
  creation, CSV import, and the administrator profile editor all refuse a
  duplicate, with a partial unique database index as the final concurrent-write
  guard. Go derives the same Unicode-normalized key for lookup, writes, and
  migration. Existing duplicates refuse migration without changing account data.
  This upgrade retires legacy sessions and credential links so historically stale
  authentication cannot survive the patch; users authenticate and request links
  again.
- An administrator may edit an account's email address and display name, and
  may set its password directly for deployments without email delivery. The
  username and role are not editable through that form. Setting a password is
  refused for the administrator's own account, which has its own path proving
  knowledge of the current password, and for a disabled account, because
  re-opening one is a separate audited decision.
- Replacing an account's credential by any route -- an administrator setting a
  password, `recover-admin`, the account holder changing their own, or a
  consumed invitation or reset link --
  retires that account's outstanding invitation and reset tokens and cancels
  any queued message still carrying one. Password replacement and required
  revocations commit in one transaction. A monotonic credential generation also
  prevents an earlier password verification or address lookup from issuing a
  session or link after the change. Credential-mail enqueue rechecks the current
  token and recipient. A password set by an administrator
  additionally must be changed at the account's next sign-in and revokes every
  session for that account, including the target's own.
- Changing an account's email address likewise invalidates its outstanding
  invitation and reset tokens and cancels queued invitation or reset mail
  addressed to the previous address; lifecycle notices, which carry no token,
  are unaffected.
- Reset requests are throttled per account. The response is identical whether
  or not the throttle applied, so the endpoint stays non-enumerating. The
  administrator-triggered reset is not throttled and names its refusal reason,
  because the caller is authenticated and already sees the account.
- Changing a password (whether voluntarily or during the mandatory first-login
  change) revokes every other session for that account while preserving the
  session performing the change, so a stolen session cannot outlive a password
  change. Disabling a user makes existing sessions unusable through the session
  lookup, which excludes disabled accounts.
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
  shell, and display-name fields. Rootless Podman runs such a workspace in a
  private user namespace with explicit UID/GID mappings derived from its
  subordinate-ID map. The COWS host account is intentionally not mapped into
  the container, so container root, including a UID 0 terminal, is a
  subordinate host ID; container-owned mount contents therefore use
  subordinate host IDs, and the file helper acts as the same translated
  identity. The opt-in live Podman test verifies the applied mapping, because
  Libpod silently ignores request fields it does not apply. The per-container parent remains
  COWS-owned for lifecycle moves. Rootless Podman resolves the passwd-entry
  extension server-side; users never select it. The adapter fails closed on
  Podman releases older than 4.1.0, which would silently ignore the passwd
  entry instead of rejecting it.
- COWS validates Podman's reported rootless UID/GID maps before using them:
  container ID ranges must start at zero and be contiguous, host ranges must
  not overlap or overflow, namespace ID zero is a single reserved slot, and no
  subordinate range may contain the COWS service account. The file helper
  translates a configured container identity through the same explicit map,
  verifies `/proc/self/{uid,gid}_map` against the host IDs the adapter derived,
  rejects a symlinked or non-directory root, resets supplementary groups, and
  receives a minimal environment without COWS configuration or
  credential-shaped variables. Configured-user file access must drop to a
  nonzero mapped identity. Only image-identity file access and
  administrator-retained-volume recovery may explicitly authorize
  namespace-root access; that path is still rooted and server-selected.
- A template may explicitly allow terminal sessions for selected container UIDs
  through `terminal_uids`. The browser may request one of those values, but the
  backend revalidates the allowlist before the Podman exec request. Allowing UID
  0 grants root inside that workspace, including control of writable mounts and
  container-visible secrets. For a template with `container_user` and the
  private namespace, that root is a subordinate host ID. For a template without
  `container_user`, Podman's default rootless namespace maps container UID 0 to
  the COWS service account; the template editor warns about this high-risk
  choice. It does not grant host root outside the container's mounts, but it
  remains inappropriate for untrusted templates that expose host paths, runtime
  sockets, devices, unsafe capabilities, or vulnerable workloads.

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
existing restrictive database-file permissions. Terminal disconnects first
terminate the server-created Podman exec process group while its upgraded
stream is attached, then close the transport and record the audit event.
Cleanup invokes trusted `/bin/kill` directly with inherited environment cleared,
never the user-configured shell. Mounts covering system executable/library trees
or loader configuration are rejected. Approved images must keep `/bin/kill` and
its dependencies outside user-writable storage. Independent process groups are
outside this cleanup scope. Failure to clean up is emitted as a structured
runtime diagnostic without terminal contents or secrets.

Generic web-application proxying and public service exposure are intentionally
not implemented. The only template service is the server-selected desktop VNC
service, which is bound to loopback and reachable through the authenticated
COWS desktop gateway only.

## Runtime and host risks

Access to rootless Podman can still become host-level access. The initial single
process therefore owns that boundary. Deployments must protect the runtime
socket and COWS data directory, use a dedicated service account where
practical, and avoid exposing the service directly without HTTPS and a trusted
reverse-proxy configuration.

The local `cows recover-admin` command resets an enabled administrator and
revokes its sessions and credential links without requiring HTTP or Podman.
Bootstrap credentials apply only to an empty database. Email recovery still
depends on an external HTTPS URL and SMTP. Bootstrap and SMTP passwords are
accepted only through protected environment configuration, never CLI arguments.

SQLite backup must account for WAL mode and must be paired with snapshots of the
managed directory and archive roots. A database-only backup cannot restore
workspace files. Named volumes live in runtime storage and are not included in
that filesystem backup procedure unless the operator separately exports them.

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
included, and no temporary archive is created. Both the in-process file
manager and the rootless file-helper subprocess stream archives through the
same `internal/archive` package, so the byte, entry, and symlink policy
cannot drift between the two paths. Archive extraction, bulk
operations, and stronger quota accounting are not implemented. Before those
features are added, include explicit ZIP-slip, ZIP-bomb, symlink replacement,
file-count, temporary-storage, and generated-download-size tests.

Explicit deletion retains named volumes and archived directories and records
tombstone metadata before the workspace row is removed. A retained-storage
record is not authorization by itself; every lookup is scoped to the
requesting party in the query, so a missing tombstone and one owned by
someone else are indistinguishable to the caller. Administrators can use the
separate recovery view to download or remove any validated volume tombstone,
with audit events. Independently, a user can browse, download, discard, or
reattach their own retained volumes and archived directories onto a new
workspace at `/storage` and from workspace creation — a separate route
namespace with no administrator bypass (decision 0025). Reattachment
consumes the tombstone atomically with the lookup (single-use), requires the
destination mount to match the tombstone's recorded name and type, and
unconditionally re-chowns a reattached volume's content to the new
container's identity (Podman's `"U"` mount option, already used for
directory mounts) since every COWS container on a host shares the same
static rootless ID mapping and only the template's configured UID/GID can
differ.

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

SMTP credentials must be protected as deployment secrets. Every SMTP exchange
has a total deadline and closes on cancellation; recipient and subject headers
are validated before connection. Credential-mail bodies contain raw bearer links
by design, including in sent/canceled rows retained by the outbox. Protect the
SQLite file and backups as credential-bearing data; hashing the dedicated token
table does not remove this separate exposure. Email warnings are
advisory and may be delayed, duplicated around process crashes, rejected by a
relay, or unavailable. They never authorize access and never determine whether
COWS stops or deletes a container. Notification messages exclude secrets,
tokens beyond their own link, terminal contents, runtime identifiers, volume
names, host paths, archive locations, and internal addresses. Link
construction refuses a base URL carrying a query or fragment, so a token
cannot be smuggled into an existing parameter. Invitation and reset links
require both email delivery and a configured external base URL; without them
those features are inert rather than half-enabled.

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
resource limits. Historical metrics, administrator-initiated retained-volume
restore on behalf of a user, stronger host-level egress policy, and deeper
runtime integration still need review. Do not deploy it as a service for
untrusted users. Tested backup/restore and complete lifecycle repair remain
incomplete. Startup does reset stale connection counts, but uncertain runtime/DB outcomes still require operator
review.

## Reporting vulnerabilities

Until a dedicated security contact is published, report suspected
vulnerabilities privately to the project maintainers rather than opening a
public issue with exploit details. Include the affected commit, reproduction
steps, impact, and any suggested mitigation. Do not include real credentials,
tokens, workspace data, or host secrets in a report.

## October 2026 hardening status

The [2026-10-15 audit disposition](docs/implementation/security-audit-2026-10-15.md)
records each implementation fix and the design risks deliberately retained.
Workspace lifecycle locks now precede global admission, with cancelable waits;
file streams cannot hold global admission while a lifecycle request waits on
them. Reconciliation discards stale observations after a concurrent lifecycle
change. Stream closure owns connection accounting exactly once and cannot
subtract a connection from a later running period.

New successful volume reattachments persist their actual source for file access,
storage accounting, recreation, and retained-storage records. Older reattachments
whose source was already lost need manual recovery. ZIP and usage walkers read
bounded directory batches before applying entry budgets, preserving rooted access
and stream byte limits. Listings reject symlinked base directories consistently
with download policy.

Responses carry a CSP that blocks foreign scripts, native inline script handlers,
objects, base URLs, framing, and foreign form destinations. The current Alpine
build still requires `unsafe-eval`, and client styling requires inline styles;
these compatibility allowances limit CSP protection. The policy permits only
same-authority HTTP/WebSocket connections, local fonts, and local/data images.
