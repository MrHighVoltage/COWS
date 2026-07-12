# ADR 0004: Workspace State Model

Status: accepted for workspace persistence

## Decision

A workspace is a COWS control-plane record owned by one user and tied to one
approved template. Creation stores the template's default resource allocation
and starts with desired state `stopped` and observed state `unknown`. No runtime
operation occurs during creation.

Desired state is the state requested by an authorized COWS operation. Observed
state is the most recent runtime report and may be `unknown` until
reconciliation. Runtime IDs and observed errors are server-side fields and are
never accepted from browser requests. Lifecycle operations may persist the
immediate runtime result, while periodic reconciliation remains authoritative
for changes made directly through Docker or Podman.

The reconciler normalizes runtime `exited` to COWS `stopped`. If a previously
managed runtime object is absent, it records `missing` and preserves the
workspace record; absence alone is not proof that deletion completed.

The initial accounting policy reserves the recorded allocation for every
workspace record, including stopped workspaces, until a later lifecycle policy
changes that explicitly. This prevents stopped records from silently consuming
untracked capacity.

## Authorization

Users may create and list only their own workspaces, and only from enabled
templates whose role policy includes their role. Administrators may inspect all
workspaces. These checks occur in the workspace service and are not delegated
to page visibility or client-side state.
