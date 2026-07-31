CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    is_admin INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

CREATE TABLE sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash TEXT NOT NULL UNIQUE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workstation_id TEXT,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    last_used_at TEXT NOT NULL,
    revoked_at TEXT
);

CREATE INDEX sessions_token_idx ON sessions(token_hash);

CREATE TABLE workstations (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    owner_user_id INTEGER NOT NULL REFERENCES users(id),
    template_id TEXT NOT NULL,
    workspace_image TEXT NOT NULL,
    state TEXT NOT NULL,
    vpn_profile_id INTEGER,
    hostname TEXT NOT NULL UNIQUE,
    cpu_limit REAL NOT NULL,
    memory_limit_mb INTEGER NOT NULL,
    pid_limit INTEGER NOT NULL,
    persistent INTEGER NOT NULL,
    vpn_required INTEGER NOT NULL DEFAULT 0,
    exit_ip TEXT NOT NULL DEFAULT '',
    expires_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    last_started_at TEXT,
    error_message TEXT NOT NULL DEFAULT ''
);
