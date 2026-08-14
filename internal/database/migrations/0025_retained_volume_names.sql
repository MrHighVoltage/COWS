ALTER TABLE retained_workspace_volumes ADD COLUMN workspace_name TEXT NOT NULL DEFAULT '';
ALTER TABLE retained_workspace_volumes ADD COLUMN template_name TEXT NOT NULL DEFAULT '';
