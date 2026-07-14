CREATE TABLE retained_workspace_volumes (
    volume_name TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    owner_user_id TEXT NOT NULL,
    template_id TEXT NOT NULL,
    mount_name TEXT NOT NULL,
    container_path TEXT NOT NULL,
    read_only INTEGER NOT NULL CHECK (read_only IN (0, 1)),
    retained_at INTEGER NOT NULL,
    UNIQUE (workspace_id, mount_name)
);

CREATE INDEX retained_workspace_volumes_workspace_idx
    ON retained_workspace_volumes(workspace_id);

CREATE INDEX retained_workspace_volumes_owner_idx
    ON retained_workspace_volumes(owner_user_id);
