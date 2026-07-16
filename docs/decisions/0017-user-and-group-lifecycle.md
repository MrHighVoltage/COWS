# ADR 0017: User and Group Lifecycle Safety

## Status

Accepted

## Context

Disabling an account must stop access immediately, but stopping its containers
is a runtime operation that can fail or be temporarily unavailable. Deleting an
account is more destructive because it also removes its workspaces and may
archive user data. Group membership affects template access and effective
quotas, while existing workspaces must remain recoverable when policy changes.

## Decision

When an administrator disables a user, COWS performs these steps:

1. In one database transaction, mark the user disabled and delete every active
   session for that user.
2. Reconcile with rootless Podman.
3. Stop every currently running workspace owned by the user.

If reconciliation or a stop fails, the user stays disabled and all sessions
remain invalid. The administrator receives an actionable error and can retry
after the runtime is available. COWS never silently re-enables the account.

An administrator can delete a user only after the user is disabled. COWS first
stops any remaining running workspaces and deletes every workspace through the
ordinary explicit deletion path. That path archives managed directory mounts,
retains named-volume tombstones, records archive activity, and cancels pending
workspace email notifications. Only after all workspace operations succeed is
the user row deleted. A failed cleanup leaves the disabled user and remaining
workspace records available for retry.

Removing a user from a group changes only membership. Existing workspaces are
not deleted or modified, although the user may lose access to templates for
future operations. Group quota allowances are recalculated immediately. If
existing allocations exceed the reduced effective quota, the allocations are
temporarily tolerated and clearly marked over quota; new workspace admission
is rejected until usage is within the effective limit.

Deleting a group removes its memberships and quota assignment. Existing
workspaces remain. Deletion is rejected while any template references the
group, because leaving a stale group reference could accidentally broaden or
misinterpret template access. An administrator must update those templates
first. Pending email notifications for deleted users are canceled.

## Consequences

- Account disablement is immediate even when the runtime is unavailable.
- Runtime cleanup is retryable and fail-closed around uncertain state.
- User deletion is intentionally multi-step and cannot be an accidental
  single-click operation.
- Existing data is preserved according to the explicit workspace deletion
  policy, rather than being silently removed by quota or group changes.
- Group deletion has a small administrative dependency: referenced templates
  must be edited before the group can be removed.

## Alternatives rejected

- Deleting a user while enabled would make active sessions and running
  workspaces harder to account for safely.
- Automatically deleting workspaces when a group is removed would be a policy
  change with an unnecessarily destructive result.
- Treating stale template group references as an empty group would fail open
  for exclusion policies.
