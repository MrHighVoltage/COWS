ALTER TABLE workspace_templates ADD COLUMN initial_connection_timeout_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workspace_templates ADD COLUMN stopped_retention_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workspace_templates ADD COLUMN data_retention_seconds INTEGER NOT NULL DEFAULT 0;

ALTER TABLE workspaces ADD COLUMN initial_connection_timeout_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workspaces ADD COLUMN stopped_retention_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workspaces ADD COLUMN data_retention_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workspaces ADD COLUMN started_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workspaces ADD COLUMN last_connected_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workspaces ADD COLUMN stopped_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workspaces ADD COLUMN container_deleted_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workspaces ADD COLUMN data_archive_eligible_at INTEGER NOT NULL DEFAULT 0;
