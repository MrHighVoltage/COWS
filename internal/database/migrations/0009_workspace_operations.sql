ALTER TABLE workspaces ADD COLUMN operation TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN operation_status TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN operation_error TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN operation_started_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workspaces ADD COLUMN operation_updated_at INTEGER NOT NULL DEFAULT 0;
