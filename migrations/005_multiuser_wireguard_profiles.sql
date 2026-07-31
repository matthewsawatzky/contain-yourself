CREATE TABLE vpn_profiles_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL COLLATE NOCASE,
    provider TEXT NOT NULL DEFAULT 'custom',
    vpn_type TEXT NOT NULL DEFAULT 'wireguard' CHECK (vpn_type = 'wireguard'),
    server_countries TEXT NOT NULL DEFAULT '',
    server_cities TEXT NOT NULL DEFAULT '',
    server_regions TEXT NOT NULL DEFAULT '',
    endpoint TEXT NOT NULL DEFAULT '',
    owner_user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
    visibility TEXT NOT NULL DEFAULT 'global' CHECK (visibility IN ('global', 'private')),
    auto_assign INTEGER NOT NULL DEFAULT 0,
    config_ref TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (
        (visibility = 'global' AND owner_user_id IS NULL) OR
        (visibility = 'private' AND owner_user_id IS NOT NULL)
    ),
    CHECK (auto_assign = 0 OR visibility = 'global')
);

INSERT INTO vpn_profiles_new (
    id, name, provider, vpn_type, server_countries, server_cities,
    server_regions, enabled, created_at, updated_at
)
SELECT
    id, name, provider,
    CASE WHEN vpn_type = 'wireguard' THEN 'wireguard' ELSE 'wireguard' END,
    server_countries, server_cities, server_regions, 0, created_at, updated_at
FROM vpn_profiles;

DROP TABLE vpn_profiles;
ALTER TABLE vpn_profiles_new RENAME TO vpn_profiles;

CREATE UNIQUE INDEX vpn_profiles_owner_name_idx
    ON vpn_profiles(COALESCE(owner_user_id, 0), name);
CREATE UNIQUE INDEX vpn_profiles_config_ref_idx
    ON vpn_profiles(config_ref) WHERE config_ref <> '';
CREATE INDEX vpn_profiles_visibility_idx
    ON vpn_profiles(visibility, enabled, auto_assign);

CREATE TABLE vpn_profile_grants (
    profile_id INTEGER NOT NULL REFERENCES vpn_profiles(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_by INTEGER NOT NULL REFERENCES users(id),
    created_at TEXT NOT NULL,
    PRIMARY KEY(profile_id, user_id)
);

CREATE INDEX vpn_profile_grants_user_idx ON vpn_profile_grants(user_id);
