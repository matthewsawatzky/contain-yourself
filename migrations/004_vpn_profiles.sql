CREATE TABLE vpn_profiles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    provider TEXT NOT NULL,
    vpn_type TEXT NOT NULL CHECK (vpn_type IN ('wireguard', 'openvpn')),
    server_countries TEXT NOT NULL DEFAULT '',
    server_cities TEXT NOT NULL DEFAULT '',
    server_regions TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX workstations_vpn_profile_idx ON workstations(vpn_profile_id);
