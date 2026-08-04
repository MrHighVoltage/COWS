# COWS Project Definition

## Vision

COWS provides one secure web interface for creating, operating, monitoring, and
accessing administrator-approved containerized workspaces. The platform should
replace collections of scripts and manually assigned ports without making the
container runtime itself part of the user-facing security boundary.

## Intended users

- Students and researchers who need repeatable software environments.
- Platform administrators who operate a Linux host and its container runtime.
- Institutions operating a COWS control plane for their users.

The first deployment may use an open-source chip-design image, but COWS is
general-purpose and must not encode assumptions about chip design, EDA, or a
particular image.

## Primary use cases

Users can select approved templates, create workspaces within their effective
quota, start and stop them, inspect state and resource use, and access an
approved terminal, graphical desktop, or file manager through COWS. The initial
desktop path is noVNC. Administrators can manage users, groups, templates,
quotas, runtime capacity, lifecycle policies, image pulls, and the runtime
overview. Workspace access is limited to the terminal, desktop, and file
manager gateways.

## Goals

- Centralize authenticated workspace access behind COWS, with HTTPS normally
  terminated by the deployment's reverse proxy.
- Keep user input away from arbitrary image names, runtime arguments, targets,
  ports, host mounts, capabilities, and devices.
- Enforce resource limits in the runtime as well as quotas in COWS.
- Separate desired state from observed runtime state and reconcile them.
- Start with a comprehensible modular monolith and SQLite.
- Use rootless Podman as the initial and only supported runtime.
- Use server-rendered HTML, HTMX, and small Alpine.js interactions without a
  frontend build process.
- Support optional, administrator-controlled local-account registration with
  server-assigned default quotas and groups.
- Provide advisory email warnings for upcoming lifecycle actions without making
  email delivery part of the lifecycle safety path.
- Provide a documented local administrator recovery path, control-plane backup
  and restore procedure, and a readiness signal before production use.
- Keep deployment simple enough for one Linux host, while documenting the
  reverse-proxy boundary needed for HTTPS.

## Non-goals for the initial milestones

- Kubernetes, microservices, clustering, or a distributed scheduler.
- Arbitrary container creation or arbitrary runtime arguments.
- Public per-workspace VNC, SSH, terminal, or application ports.
- Generic web-application proxying or arbitrary service exposure.
- Archive extraction, file previews, bulk file operations, and historical
  metrics.
- A complete permissions framework before the basic user/administrator roles
  are proven.
- Institutional identity and email verification. Local password-reset email is
  supported when explicitly configured and uses its own security design.
- PostgreSQL or high availability during the SQLite deployment phase.
- Multiple active COWS processes; admission locks and account throttles are
  process-local until a future shared coordination design exists.
- An application-specific frontend development server.

## Functional requirements

The backend must independently authorize every state-changing operation and
every access session. Workspace ownership is checked in the backend; opaque IDs
never substitute for authorization. Templates are administrator-controlled and
validated before use. CPU, memory, and process limits are enforced by the
runtime where supported. Storage is measured for display and finite user
allowances; per-template and per-workspace storage limits are not currently
runtime-enforced.

The scheduler performs deterministic quota and host-capacity checks. CPU and
memory allocations count only observed running workspaces; total and running
workspace counts are separate. Host CPU and memory admission use administrator
configured overbooking factors. Storage is not a host-capacity admission
check.

An ordinary user must have either an explicit quota or an effective quota from
one or more groups before creating a workspace. An explicit user quota
overrides group quotas. Group limits add together, while zero makes that
resource unlimited. An administrator without either quota type is unrestricted
by COWS user quotas. Physical host CPU and memory capacity apply to all
accounts. Administrators edit explicit user quotas from the user edit view and
group quotas from the group edit view; there is no separate global quota list.

Workspace lifecycle policies must support two administrator-defined durations:

- an initial connection timeout after which a workspace with no user connection
  is stopped but its container is retained;
- a stopped-container retention timeout after which the stopped container may be
  deleted; and
- explicit deletion archives managed directory data, while automatic timeout
  cleanup never deletes or archives user data. There is no post-deletion data
  retention setting.

These policies are visible to users on workspace pages. COWS may create
deduplicated advisory warning notifications for upcoming actions, but email
delivery must never decide whether a timeout is enforced.

Self-registration is disabled by default. If enabled, it requires an email
address and applies server-configured default quota values and default group
membership atomically. Newly self-registered accounts use the normal user
role. Administrator-created accounts retain the mandatory first-login password
change. Authenticated users can change their passwords, and local users can
request a non-enumerating password reset by email when it is configured.

The database is control-plane state, not a high-frequency metrics store. Runtime
observations remain authoritative for container existence and running state.
The current reconciliation worker records missing and orphaned objects but
does not automatically repair missing, orphaned, or interrupted operations.
The repair policy and restart recovery procedure remain incomplete.

`/healthz` currently provides liveness plus a SQLite connectivity check; it does
not prove that rootless Podman is reachable. A separate runtime-aware readiness
signal is required before a reverse proxy or service supervisor can safely use
it for admission readiness.

The initial deployment has no built-in backup or offline administrator
credential-recovery command. Operators must follow the documented backup
procedure and configure local password-reset email, or wait for the dedicated
recovery implementation, before treating the installation as recoverable.

## Deployment assumptions

- One Linux server and one active COWS control-plane process.
- One local rootless Podman runtime and user service socket.
- One SQLite database on local supported storage.
- A reverse proxy may terminate HTTPS.
- Frontend assets are embedded or served locally; no CDN is required.
- No Node.js, npm, Kubernetes, or shared network filesystem is required.
- Docker is not a supported runtime; deployments use rootless Podman.
- Templates without the desktop service use `none` networking. Desktop-enabled
  templates use loopback-only host mapping; optional per-workspace internal
  network isolation can be enabled for new desktop-enabled workspaces.

## Terminology

- **Workspace**: A user-owned, COWS-managed containerized environment.
- **Template**: An administrator-approved, validated workspace definition.
- **Desired state**: The lifecycle state COWS is asking the runtime to provide.
- **Observed state**: The state most recently reported by the runtime.
- **Quota**: A COWS policy limiting a user's or group's allocations; explicit
  user quotas override inherited group quotas.
- **Allocated resources**: Resources reserved for workspaces under the policy.
- **Consumed resources**: Resources currently reported as in use by the runtime.
- **Access gateway**: Authenticated COWS routing for terminal, desktop, or file
  manager sessions.
- **Managed container**: A runtime object identified by COWS labels or their
  Podman equivalent.
- **Initial connection timeout**: The maximum time a newly started workspace
  may remain without an authenticated user connection before COWS stops it.
- **Stopped retention**: The period a stopped container remains available
  before deletion becomes due.
- **Measured storage usage**: Runtime-reported container writable-layer usage
  plus helper-measured managed mount and volume usage, excluding the image.
