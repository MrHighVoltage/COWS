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

Users will eventually be able to select an approved template, create a
workspace within their quota, start and stop it, inspect its state and resource
use, and access an approved terminal, graphical desktop, or web application
through COWS. Administrators will eventually manage users, templates, quotas,
runtime capacity, policies, and audit information.

## Goals

- Centralize authenticated workspace access behind COWS HTTPS.
- Keep user input away from arbitrary image names, runtime arguments, targets,
  ports, host mounts, capabilities, and devices.
- Enforce resource limits in the runtime as well as quotas in COWS.
- Separate desired state from observed runtime state and reconcile them.
- Start with a comprehensible modular monolith and SQLite.
- Use rootless Podman as the initial and only supported runtime.
- Use server-rendered HTML, HTMX, and small Alpine.js interactions without a
  frontend build process.

## Non-goals for the initial milestones

- Kubernetes, microservices, clustering, or a distributed scheduler.
- Arbitrary container creation or arbitrary runtime arguments.
- Public per-workspace VNC, SSH, terminal, or application ports.
- Full file management, uploads, archive extraction, or historical metrics.
- A complete permissions framework before the basic user/administrator roles
  are proven.
- PostgreSQL or high availability during the SQLite deployment phase.
- An application-specific frontend development server.

## Functional requirements

The backend must independently authorize every state-changing operation and
every access session. Workspace ownership is checked in the backend; opaque IDs
never substitute for authorization. Templates are administrator-controlled and
validated before use. Runtime-enforced CPU, memory, process, storage, and other
applicable limits must correspond to the quota and template policy.

The scheduler initially performs deterministic quota and host-capacity checks,
with no unsafe overcommit by default. Accounting policy must state whether
stopped workspaces continue to reserve resources.

An ordinary user must have an assigned quota before creating a workspace. An
administrator without a quota assignment is unrestricted by COWS user quotas.
Within an assigned quota, zero means unlimited for that resource; physical host
capacity and reserved host capacity still apply to all accounts.

Workspace lifecycle policies must support two administrator-defined durations:

- an initial connection timeout after which a workspace with no user connection
  is stopped but its container is retained;
- a stopped-container retention timeout after which the stopped container may be
  deleted; and
- explicit deletion archives managed directory data, while automatic timeout
  cleanup never deletes or archives user data. There is no post-deletion data
  retention setting.

These policies are visible to users on workspace pages. COWS does not archive
data or send email in the initial implementation, but policy evaluation must
leave explicit hooks for future warning notifications and audit events.

The database is control-plane state, not a high-frequency metrics store. Runtime
observations remain authoritative for container existence and running state.

## Deployment assumptions

- One Linux server and one active COWS control-plane process.
- One local rootless Podman runtime and user service socket.
- One SQLite database on local supported storage.
- A reverse proxy may terminate HTTPS.
- Frontend assets are embedded or served locally; no CDN is required.
- No Node.js, npm, Kubernetes, or shared network filesystem is required.

## Terminology

- **Workspace**: A user-owned, COWS-managed containerized environment.
- **Template**: An administrator-approved, validated workspace definition.
- **Desired state**: The lifecycle state COWS is asking the runtime to provide.
- **Observed state**: The state most recently reported by the runtime.
- **Quota**: A COWS policy limiting a user's or group’s allocations.
- **Allocated resources**: Resources reserved for workspaces under the policy.
- **Consumed resources**: Resources currently reported as in use by the runtime.
- **Access gateway**: Authenticated COWS routing for terminal, desktop, or web
  application sessions.
- **Managed container**: A runtime object identified by COWS labels or their
  Podman equivalent.
- **Initial connection timeout**: The maximum time a newly started workspace
  may remain without an authenticated user connection before COWS stops it.
- **Stopped retention**: The period a stopped container remains available
  before deletion becomes due.
- **Measured storage usage**: Runtime-reported container writable-layer usage
  plus helper-measured managed mount and volume usage, excluding the image.
