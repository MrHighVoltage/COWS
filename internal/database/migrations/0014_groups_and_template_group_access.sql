CREATE TABLE IF NOT EXISTS groups (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    description TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS user_groups (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, group_id)
);

CREATE INDEX IF NOT EXISTS user_groups_group_idx ON user_groups(group_id);
ALTER TABLE workspace_templates ADD COLUMN group_access_mode TEXT NOT NULL DEFAULT 'exclude';
ALTER TABLE workspace_templates ADD COLUMN allowed_group_ids_json TEXT NOT NULL DEFAULT '[]';
