# COWS Architecture

## Shape

COWS starts as a modular monolith. One process contains the HTTP service,
server-rendered web interface, domain services, SQLite repositories, runtime
adapter, and reconciliation worker. Internal interfaces mark boundaries that
may later become a privileged host agent or another deployable component, but
there is no distributed system in the first milestone.

```mermaid
flowchart LR
    Browser[Browser] -->|HTTPS only| HTTP[HTTP server]
    HTTP --> Auth[Authentication and authorization]
    HTTP --> Web[Go templates and HTMX fragments]
    HTTP --> Access[Access gateway]
    Auth --> Services[Domain services]
    Web --> Services
    Access --> Services
    Services --> Repo[Focused SQLite repositories]
    Services --> Runtime[Runtime interface]
    Runtime --> Docker[Docker adapter]
    Runtime --> Podman[Podman adapter]
    Runtime --> Container[Private managed containers]
    Reconcile[Reconciliation worker] --> Runtime
    Reconcile --> Repo
```

## Security boundaries

The public boundary is the COWS HTTPS endpoint. The browser supplies user
intent, not a container ID, backend address, port, image, or runtime argument.
Handlers authenticate the request and authorize the concrete operation before
calling a domain service. Domain services repeat ownership and policy checks
so a future handler cannot accidentally create a bypass.

The runtime adapter is the only application boundary allowed to communicate
with Docker or Podman. The runtime socket should be available only to the COWS
process, or later to a narrowly privileged host agent. Containers use private
networking where practical; no workspace port is a public routing mechanism.

Terminal, desktop, and proxy sessions are authenticated COWS sessions whose
targets are selected from server-side workspace records and template policy.
They are not generic reverse proxies.

## Request and state flow

```mermaid
sequenceDiagram
    participant U as Browser
    participant C as COWS
    participant D as Docker/Podman
    participant DB as SQLite
    U->>C: Authenticated lifecycle request
    C->>DB: Load workspace, owner, policy, desired state
    C->>C: Authorize and check quota/capacity
    C->>DB: Persist desired state and operation metadata
    C->>D: Idempotent runtime operation
    D-->>C: Runtime result and observed state
    C->>DB: Persist observed state
    C-->>U: Rendered HTML fragment or access session
```

Desired and observed state are separate fields. A runtime restart, manual
deletion, partial failure, or COWS restart must not be hidden by treating the
database as proof that a container exists. Reconciliation periodically lists
managed runtime objects, matches them using COWS labels, updates observed
state, records anomalies, and applies a documented policy to orphaned or
missing objects. Lifecycle operations should be idempotent where the runtime
allows it.

## Workspace timeout model

Timeouts are administrator-controlled template policy, stored as durations in
the workspace record when the workspace is created. This makes the effective
policy visible and stable even if an administrator later changes a template.
The initial lifecycle policy has three independent phases:

1. If a running workspace has never recorded an authenticated user connection
   and its initial connection deadline expires, COWS stops the container and
   retains the workspace and container record.
2. Once a container is stopped, its stopped deadline determines when COWS may
   delete the container. The workspace control-plane record remains so the
   result is observable and reconciliation can finish safely.
3. After container deletion, the data-retention deadline determines when
   leftover data, such as named volumes, becomes archive-eligible. The current
   milestone only records that eligibility; it performs no archive or deletion
   action on user data.

The lifecycle worker evaluates these deadlines on the server. A browser timer
is never authoritative. User pages show the effective durations, current phase,
and any due or upcoming deadline. The policy model leaves room for future
warning events and email delivery without making email a lifecycle dependency.

## Packages and ownership

The initial package layout stays small:

```text
cmd/cows/              process startup, bootstrap, and graceful shutdown
internal/config/       configuration parsing and validation
internal/database/     SQLite setup and migrations
internal/web/          handlers, templates, and static asset serving
internal/domain/       stable COWS concepts and error categories
internal/auth/         password authentication and session use cases
internal/repository/   focused persistence interfaces and SQLite implementation
internal/runtime/      Docker and Podman domain adapter boundary
internal/runtime/docker/ Docker Engine API adapter
migrations/            ordered SQL migrations
web/                   templates and local browser assets
docs/decisions/        short architecture decisions
```

Packages should be created when they contain meaningful code. Avoid a broad
generic database wrapper and avoid leaking Docker-specific types into domain or
HTTP packages.

## Database direction

SQLite uses WAL mode, foreign keys, a busy timeout, application-controlled
migrations, and a local supported filesystem. The first schema migration only
proves the migration mechanism. The later control-plane schema is expected to
contain users, roles, templates, template access rules, workspaces, desired and
observed state, effective timeout policy and lifecycle timestamps, quota
assignments, runtime identifiers, access sessions, policy configuration, hosts,
and structured audit events.

Repository methods should represent domain operations rather than expose SQL
details. PostgreSQL is a future option when multiple active control-plane
instances or higher availability requirements justify it.

Workspace templates are administrator-controlled records. Their current policy
surface contains an image reference and optional immutable digest, CPU/memory/
storage defaults and maxima, supported access-method names, allowed roles,
enabled state, and initial-connection, stopped-retention, and data-retention
durations. JSON columns store the small access-method and role lists for the
initial SQLite deployment; browser input is converted to typed values and
validated before persistence. Runtime arguments, mounts, capabilities, devices,
host networking, and arbitrary environment values are intentionally absent.

Workspaces reference an owner and template through foreign keys. Creation
stores the template's default allocations and desired state `stopped`; it does
not contact Docker. Observed state, runtime ID, and reconciliation errors are
stored separately and may only be updated by authorized runtime lifecycle or
reconciliation code. Ordinary users can list and access their own records;
administrators can inspect all records through service-layer authorization.

The reconciliation worker periodically performs a validated runtime inspection
and persists observed state. Docker/Podman `exited` is normalized to COWS
`stopped`; a managed object that is absent is recorded as `missing` without
being treated as deleted. Timeout actions run only after a successful
reconciliation pass.

Quota checks use recorded allocations for all existing workspaces, including
stopped records. A request must fit the user's CPU, memory, storage, and
workspace-count quota and the remaining host capacity after reserved capacity.
Missing quotas block ordinary users but do not restrict administrators. A zero
value in an assigned quota means unlimited for that dimension; host capacity
checks still apply to administrators.
The scheduler does not overcommit by default. Docker reports CPU and memory;
allocatable storage is an explicit host setting because Docker's host info
response does not provide a portable allocatable-storage contract. The
`COWS_HOST_STORAGE_BYTES` configuration value seeds that setting on first
startup, but the persisted row is the source of truth afterward. Administrators
can update storage capacity and reserved resources through the web UI without
restarting the service. Unknown capacity fails closed, and updates are audited.

## Frontend architecture

Go `html/template` renders complete pages, layouts, components, and fragments.
HTMX submits forms and replaces server-rendered fragments. Alpine.js is limited
to ephemeral browser-local state such as dialogs or tabs; it is never the source
of authoritative workspace state. Browser libraries are pinned, stored locally,
and served from COWS. There is no Node.js, npm, TypeScript, bundler, or required
frontend compilation step.

HTMX and Alpine.js are the only browser dependencies in the foundation. Web
Awesome is the preferred candidate for a later standards-based component
library, but it is not added until its self-hosted distribution, licensing, and
update procedure are verified against the repository policy. The initial page
uses semantic HTML and a small project stylesheet so the library decision does
not become an unreviewed runtime dependency.

## Future host-agent boundary

The runtime interface is intentionally internal and domain-oriented. It keeps
Docker-specific objects inside the adapter. The Docker adapter supports
server-selected create, start, stop, remove, version, host-capacity,
managed-list, and inspect requests. The timeout worker performs stop/remove
only after the workspace service verifies state and deadlines. A future host
agent may own runtime socket access, host registration, capability reporting,
and remote lifecycle operations. That is an
evolution point, not a reason to add RPC, a message broker, or multiple services
now.

## Operational direction

Use structured `log/slog` logging with request correlation IDs and safe context.
Expose a health endpoint in Milestone 0. Later readiness should include database
and runtime connectivity, and metrics should be sampled in memory or delegated
to a monitoring system rather than written on every sample to SQLite.
