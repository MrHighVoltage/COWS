CREATE TABLE IF NOT EXISTS group_quotas (
    group_id TEXT PRIMARY KEY REFERENCES groups(id) ON DELETE CASCADE,
    max_cpu_millis INTEGER NOT NULL DEFAULT 0,
    max_memory_bytes INTEGER NOT NULL DEFAULT 0,
    max_storage_bytes INTEGER NOT NULL DEFAULT 0,
    max_workspaces INTEGER NOT NULL DEFAULT 0,
    max_running_workspaces INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
