CREATE TABLE retained_workspace_directories (
    workspace_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    template_id TEXT NOT NULL,
    template_name TEXT NOT NULL,
    workspace_name TEXT NOT NULL,
    archive_path TEXT NOT NULL,
    mounts_json TEXT NOT NULL,
    retained_at INTEGER NOT NULL
);

CREATE INDEX retained_workspace_directories_owner_idx
    ON retained_workspace_directories(owner_user_id);
