ALTER TABLE workspace_templates ADD COLUMN resources_configurable INTEGER NOT NULL DEFAULT 0 CHECK (resources_configurable IN (0, 1));
