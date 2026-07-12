CREATE TABLE IF NOT EXISTS host_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    host_storage_bytes INTEGER NOT NULL DEFAULT 0,
    reserved_cpu_millis INTEGER NOT NULL DEFAULT 0,
    reserved_memory_bytes INTEGER NOT NULL DEFAULT 0,
    reserved_storage_bytes INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
