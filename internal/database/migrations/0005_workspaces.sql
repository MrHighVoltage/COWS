CREATE TABLE IF NOT EXISTS workspaces (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    template_id TEXT NOT NULL REFERENCES workspace_templates(id) ON DELETE RESTRICT,
    name TEXT NOT NULL,
    desired_state TEXT NOT NULL CHECK (desired_state IN ('stopped', 'running')),
    observed_state TEXT NOT NULL DEFAULT 'unknown',
    runtime_id TEXT NOT NULL DEFAULT '',
    observed_error TEXT NOT NULL DEFAULT '',
    allocated_cpu_millis INTEGER NOT NULL,
    allocated_memory_bytes INTEGER NOT NULL,
    allocated_storage_bytes INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    observed_at INTEGER NOT NULL DEFAULT 0,
    UNIQUE (owner_user_id, name)
);

CREATE INDEX IF NOT EXISTS workspaces_owner_idx ON workspaces(owner_user_id);
CREATE INDEX IF NOT EXISTS workspaces_template_idx ON workspaces(template_id);
