-- Migration 0026 added idle_since with a DEFAULT of 0, so every workspace that
-- already existed when it was applied looks "never idle" to
-- workspace.EvaluateTimeouts and is permanently exempt from the idle stop.
-- Seed those rows from started_at, which restores exactly the pre-idle_since
-- behaviour (the timeout used to be measured from the start) instead of
-- granting every existing workspace a fresh full timeout.
UPDATE workspaces
SET idle_since = started_at
WHERE idle_since = 0
  AND active_sessions = 0
  AND started_at > 0
  AND observed_state = 'running';
