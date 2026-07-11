CREATE TABLE IF NOT EXISTS workspace_templates (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    description TEXT NOT NULL,
    image_reference TEXT NOT NULL,
    image_digest TEXT NOT NULL DEFAULT '',
    default_cpu_millis INTEGER NOT NULL,
    max_cpu_millis INTEGER NOT NULL,
    default_memory_bytes INTEGER NOT NULL,
    max_memory_bytes INTEGER NOT NULL,
    default_storage_bytes INTEGER NOT NULL,
    access_methods_json TEXT NOT NULL,
    allowed_roles_json TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS workspace_templates_enabled_idx ON workspace_templates(enabled);
