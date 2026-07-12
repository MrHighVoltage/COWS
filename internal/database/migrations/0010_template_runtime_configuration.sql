ALTER TABLE workspace_templates ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE workspace_templates ADD COLUMN configuration_json TEXT NOT NULL DEFAULT '{}';

ALTER TABLE workspaces ADD COLUMN template_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workspaces ADD COLUMN template_configuration_json TEXT NOT NULL DEFAULT '{}';

CREATE TABLE IF NOT EXISTS workspace_port_allocations (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    service_name TEXT NOT NULL,
    protocol TEXT NOT NULL CHECK (protocol IN ('tcp', 'udp')),
    container_port INTEGER NOT NULL CHECK (container_port BETWEEN 1 AND 65535),
    port_pool TEXT NOT NULL,
    host_port INTEGER NOT NULL CHECK (host_port BETWEEN 1024 AND 65535),
    PRIMARY KEY (workspace_id, service_name),
    UNIQUE (protocol, host_port)
);

CREATE INDEX IF NOT EXISTS workspace_port_allocations_workspace_idx
    ON workspace_port_allocations(workspace_id);
