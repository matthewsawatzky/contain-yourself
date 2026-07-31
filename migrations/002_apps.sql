CREATE TABLE workstation_apps (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workstation_id TEXT NOT NULL REFERENCES workstations(id) ON DELETE CASCADE,
    app_id TEXT NOT NULL,
    app_version TEXT NOT NULL,
    state TEXT NOT NULL,
    container_name TEXT NOT NULL DEFAULT '',
    internal_port INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    error_message TEXT NOT NULL DEFAULT '',
    UNIQUE(workstation_id, app_id)
);

CREATE TABLE app_registry (
    app_id TEXT PRIMARY KEY,
    version TEXT NOT NULL,
    manifest_json TEXT NOT NULL,
    valid INTEGER NOT NULL,
    validation_error TEXT NOT NULL DEFAULT '',
    scanned_at TEXT NOT NULL
);

CREATE TABLE templates (
    template_id TEXT PRIMARY KEY,
    manifest_json TEXT NOT NULL,
    valid INTEGER NOT NULL,
    validation_error TEXT NOT NULL DEFAULT '',
    scanned_at TEXT NOT NULL
);

CREATE TABLE events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workstation_id TEXT REFERENCES workstations(id) ON DELETE SET NULL,
    event_type TEXT NOT NULL,
    message TEXT NOT NULL,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);

CREATE INDEX events_workstation_idx ON events(workstation_id, id DESC);

CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
