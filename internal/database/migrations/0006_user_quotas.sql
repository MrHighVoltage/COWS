CREATE TABLE IF NOT EXISTS user_quotas (
    user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    max_cpu_millis INTEGER NOT NULL,
    max_memory_bytes INTEGER NOT NULL,
    max_storage_bytes INTEGER NOT NULL,
    max_workspaces INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
