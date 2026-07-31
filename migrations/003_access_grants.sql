CREATE TABLE access_grants (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workstation_id TEXT NOT NULL REFERENCES workstations(id) ON DELETE CASCADE,
    principal_type TEXT NOT NULL,
    principal_id TEXT NOT NULL,
    permissions TEXT NOT NULL,
    expires_at TEXT,
    revoked_at TEXT,
    created_at TEXT NOT NULL
);

CREATE TABLE share_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workstation_id TEXT NOT NULL REFERENCES workstations(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    permissions TEXT NOT NULL,
    named_recipient TEXT NOT NULL DEFAULT '',
    expires_at TEXT,
    max_uses INTEGER,
    use_count INTEGER NOT NULL DEFAULT 0,
    created_by INTEGER NOT NULL REFERENCES users(id),
    created_at TEXT NOT NULL,
    last_used_at TEXT,
    revoked_at TEXT
);
