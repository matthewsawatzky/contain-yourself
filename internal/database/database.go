// Package database owns SQLite access, migrations, integrity checks, and
// transactional persistence.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"workstation-manager/internal/sharing"
	schema "workstation-manager/migrations"

	_ "modernc.org/sqlite"
)

type DB struct {
	sql *sql.DB
}

type User struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	IsAdmin   bool      `json:"is_admin"`
	CreatedAt time.Time `json:"created_at"`
}

type Workstation struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	OwnerUserID    int64            `json:"owner_user_id"`
	TemplateID     string           `json:"template_id"`
	WorkspaceImage string           `json:"workspace_image"`
	State          string           `json:"state"`
	Hostname       string           `json:"hostname"`
	CPULimit       float64          `json:"cpu_limit"`
	MemoryLimitMB  int              `json:"memory_limit_mb"`
	PIDLimit       int              `json:"pid_limit"`
	Persistent     bool             `json:"persistent"`
	VPNRequired    bool             `json:"vpn_required"`
	VPNProfileID   *int64           `json:"vpn_profile_id,omitempty"`
	ExitIP         string           `json:"exit_ip,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
	LastStartedAt  time.Time        `json:"last_started_at,omitempty"`
	ErrorMessage   string           `json:"error_message,omitempty"`
	Apps           []WorkstationApp `json:"apps,omitempty"`
}

type VPNProfile struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Provider        string    `json:"provider"`
	VPNType         string    `json:"vpn_type"`
	ServerCountries string    `json:"server_countries,omitempty"`
	ServerCities    string    `json:"server_cities,omitempty"`
	ServerRegions   string    `json:"server_regions,omitempty"`
	Endpoint        string    `json:"endpoint,omitempty"`
	OwnerUserID     *int64    `json:"owner_user_id,omitempty"`
	Visibility      string    `json:"visibility"`
	AutoAssign      bool      `json:"auto_assign"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ConfigRef       string    `json:"-"`
}

type WorkstationApp struct {
	AppID         string `json:"app_id"`
	AppVersion    string `json:"app_version"`
	State         string `json:"state"`
	ContainerName string `json:"container_name,omitempty"`
	InternalPort  int    `json:"internal_port"`
}

type Event struct {
	ID            int64     `json:"id"`
	WorkstationID string    `json:"workstation_id"`
	Type          string    `json:"type"`
	Message       string    `json:"message"`
	CreatedAt     time.Time `json:"created_at"`
}

type Share struct {
	ID             int64                `json:"id"`
	WorkstationID  string               `json:"workstation_id"`
	Permissions    []sharing.Permission `json:"permissions"`
	NamedRecipient string               `json:"named_recipient,omitempty"`
	ExpiresAt      *time.Time           `json:"expires_at,omitempty"`
	MaxUses        *int                 `json:"max_uses,omitempty"`
	UseCount       int                  `json:"use_count"`
	CreatedBy      int64                `json:"created_by"`
	CreatedAt      time.Time            `json:"created_at"`
	LastUsedAt     *time.Time           `json:"last_used_at,omitempty"`
	RevokedAt      *time.Time           `json:"revoked_at,omitempty"`
}

func Open(ctx context.Context, path string) (*DB, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	db := &DB{sql: sqlDB}
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := sqlDB.ExecContext(ctx, pragma); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	if err := db.migrate(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	if err := db.IntegrityCheck(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	if path != ":memory:" {
		for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
			if err := os.Chmod(candidate, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
				sqlDB.Close()
				return nil, fmt.Errorf("secure database file %s: %w", candidate, err)
			}
		}
	}
	return db, nil
}

func (db *DB) Close() error { return db.sql.Close() }

func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.sql.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}
	migrations, err := schema.All()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	for index, migration := range migrations {
		version := index + 1
		var count int
		if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, migration); err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)", version, now())
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}

func (db *DB) IntegrityCheck(ctx context.Context) error {
	var result string
	if err := db.sql.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("sqlite integrity check failed: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("sqlite corruption detected: %s", result)
	}
	return nil
}

func (db *DB) Backup(ctx context.Context, destination string) error {
	if strings.TrimSpace(destination) == "" {
		return errors.New("backup destination is required")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	escaped := strings.ReplaceAll(destination, "'", "''")
	if _, err := db.sql.ExecContext(ctx, "VACUUM INTO '"+escaped+"'"); err != nil {
		return fmt.Errorf("backup sqlite database: %w", err)
	}
	return os.Chmod(destination, 0o600)
}

func (db *DB) HasUsers(ctx context.Context) (bool, error) {
	var count int
	err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count)
	return count > 0, err
}

func (db *DB) CreateInitialAdmin(ctx context.Context, username, passwordHash string) (User, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return User{}, err
	}
	if count != 0 {
		return User{}, errors.New("initial administrator already exists")
	}
	result, err := tx.ExecContext(ctx,
		"INSERT INTO users(username, password_hash, is_admin, created_at) VALUES(?, ?, 1, ?)",
		username, passwordHash, now())
	if err != nil {
		return User{}, err
	}
	id, _ := result.LastInsertId()
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return User{ID: id, Username: username, IsAdmin: true}, nil
}

func (db *DB) Authenticate(ctx context.Context, username string) (User, string, error) {
	var user User
	var passwordHash string
	err := db.sql.QueryRowContext(ctx,
		"SELECT id, username, password_hash, is_admin FROM users WHERE username = ?",
		username).Scan(&user.ID, &user.Username, &passwordHash, &user.IsAdmin)
	return user, passwordHash, err
}

func (db *DB) CreateUser(ctx context.Context, username, passwordHash string, isAdmin bool) (User, error) {
	stamp := now()
	result, err := db.sql.ExecContext(ctx,
		"INSERT INTO users(username, password_hash, is_admin, created_at) VALUES(?, ?, ?, ?)",
		username, passwordHash, isAdmin, stamp)
	if err != nil {
		return User{}, err
	}
	id, _ := result.LastInsertId()
	created, _ := time.Parse(time.RFC3339Nano, stamp)
	return User{ID: id, Username: username, IsAdmin: isAdmin, CreatedAt: created}, nil
}

func (db *DB) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := db.sql.QueryContext(ctx,
		"SELECT id, username, is_admin, created_at FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var user User
		var created string
		if err := rows.Scan(&user.ID, &user.Username, &user.IsAdmin, &created); err != nil {
			return nil, err
		}
		user.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		users = append(users, user)
	}
	return users, rows.Err()
}

func (db *DB) CreateSession(ctx context.Context, userID int64, tokenHash string, expires time.Time) error {
	_, err := db.sql.ExecContext(ctx, `INSERT INTO sessions
		(token_hash, user_id, expires_at, created_at, last_used_at)
		VALUES(?, ?, ?, ?, ?)`, tokenHash, userID, formatTime(expires), now(), now())
	return err
}

func (db *DB) SessionUser(ctx context.Context, tokenHash string) (User, error) {
	var user User
	err := db.sql.QueryRowContext(ctx, `SELECT u.id, u.username, u.is_admin
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.revoked_at IS NULL AND s.expires_at > ?`,
		tokenHash, now()).Scan(&user.ID, &user.Username, &user.IsAdmin)
	if err == nil {
		_, _ = db.sql.ExecContext(ctx, "UPDATE sessions SET last_used_at = ? WHERE token_hash = ?", now(), tokenHash)
	}
	return user, err
}

func (db *DB) RevokeSession(ctx context.Context, tokenHash string) error {
	_, err := db.sql.ExecContext(ctx,
		"UPDATE sessions SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL", now(), tokenHash)
	return err
}

func (db *DB) RevokeUserSessions(ctx context.Context, userID int64) error {
	_, err := db.sql.ExecContext(ctx,
		"UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL", now(), userID)
	return err
}

func (db *DB) CreateVPNProfile(ctx context.Context, profile VPNProfile) (VPNProfile, error) {
	stamp := now()
	result, err := db.sql.ExecContext(ctx, `INSERT INTO vpn_profiles
		(name, provider, vpn_type, server_countries, server_cities, server_regions,
		 endpoint, owner_user_id, visibility, auto_assign, config_ref, enabled,
		 created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		profile.Name, profile.Provider, profile.VPNType, profile.ServerCountries,
		profile.ServerCities, profile.ServerRegions, profile.Endpoint,
		profile.OwnerUserID, profile.Visibility, profile.AutoAssign,
		profile.ConfigRef, profile.Enabled, stamp, stamp)
	if err != nil {
		return VPNProfile{}, err
	}
	id, _ := result.LastInsertId()
	return db.VPNProfile(ctx, id)
}

func (db *DB) ListVPNProfiles(ctx context.Context, enabledOnly bool) ([]VPNProfile, error) {
	query := vpnProfileSelect
	if enabledOnly {
		query += " WHERE enabled = 1"
	}
	query += " ORDER BY name"
	rows, err := db.sql.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []VPNProfile
	for rows.Next() {
		profile, err := scanVPNProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (db *DB) VPNProfile(ctx context.Context, id int64) (VPNProfile, error) {
	return scanVPNProfile(db.sql.QueryRowContext(ctx, vpnProfileSelect+" WHERE id = ?", id))
}

func (db *DB) ListVPNProfilesForUser(ctx context.Context, user User, enabledOnly bool) ([]VPNProfile, error) {
	query := vpnProfileSelect + ` WHERE (
		visibility = 'global' OR owner_user_id = ? OR EXISTS (
			SELECT 1 FROM vpn_profile_grants g
			WHERE g.profile_id = vpn_profiles.id AND g.user_id = ?
		)
	)`
	args := []any{user.ID, user.ID}
	if enabledOnly {
		query += " AND enabled = 1"
	}
	query += " ORDER BY auto_assign DESC, visibility, name"
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []VPNProfile
	for rows.Next() {
		profile, err := scanVPNProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (db *DB) VPNProfileForUser(ctx context.Context, id int64, user User) (VPNProfile, error) {
	return scanVPNProfile(db.sql.QueryRowContext(ctx, vpnProfileSelect+` WHERE id = ? AND (
		visibility = 'global' OR owner_user_id = ? OR EXISTS (
			SELECT 1 FROM vpn_profile_grants g
			WHERE g.profile_id = vpn_profiles.id AND g.user_id = ?
		)
	)`, id, user.ID, user.ID))
}

func (db *DB) SetVPNProfileEnabled(ctx context.Context, id int64, user User, enabled bool) error {
	query := "UPDATE vpn_profiles SET enabled = ?, updated_at = ? WHERE id = ?"
	args := []any{enabled, now(), id}
	if !user.IsAdmin {
		query += " AND owner_user_id = ?"
		args = append(args, user.ID)
	}
	result, err := db.sql.ExecContext(ctx,
		query, args...)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return sql.ErrNoRows
	}
	return nil
}

const vpnProfileSelect = `SELECT id, name, provider, vpn_type, server_countries,
	server_cities, server_regions, endpoint, owner_user_id, visibility,
	auto_assign, config_ref, enabled, created_at, updated_at FROM vpn_profiles`

func scanVPNProfile(row scanner) (VPNProfile, error) {
	var profile VPNProfile
	var created, updated string
	var ownerUserID sql.NullInt64
	if err := row.Scan(&profile.ID, &profile.Name, &profile.Provider, &profile.VPNType,
		&profile.ServerCountries, &profile.ServerCities, &profile.ServerRegions,
		&profile.Endpoint, &ownerUserID, &profile.Visibility, &profile.AutoAssign,
		&profile.ConfigRef, &profile.Enabled, &created, &updated); err != nil {
		return VPNProfile{}, err
	}
	if ownerUserID.Valid {
		profile.OwnerUserID = &ownerUserID.Int64
	}
	profile.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	profile.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return profile, nil
}

func (db *DB) CreateShare(ctx context.Context, workstationID string, createdBy int64,
	tokenHash string, permissions []sharing.Permission, recipient string,
	expiresAt *time.Time, maxUses *int) (Share, error) {
	encoded, err := sharing.Encode(permissions)
	if err != nil {
		return Share{}, err
	}
	var expires any
	if expiresAt != nil {
		expires = formatTime(*expiresAt)
	}
	var uses any
	if maxUses != nil {
		uses = *maxUses
	}
	result, err := db.sql.ExecContext(ctx, `INSERT INTO share_tokens
		(workstation_id, token_hash, permissions, named_recipient, expires_at,
		 max_uses, created_by, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		workstationID, tokenHash, encoded, recipient, expires, uses, createdBy, now())
	if err != nil {
		return Share{}, err
	}
	id, _ := result.LastInsertId()
	return db.shareByID(ctx, id)
}

func (db *DB) ListShares(ctx context.Context, workstationID string) ([]Share, error) {
	rows, err := db.sql.QueryContext(ctx, shareSelect+
		" WHERE workstation_id = ? ORDER BY id DESC", workstationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Share
	for rows.Next() {
		share, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, share)
	}
	return result, rows.Err()
}

// RedeemShare consumes one use atomically and returns the effective share.
func (db *DB) RedeemShare(ctx context.Context, tokenHash string) (Share, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return Share{}, err
	}
	defer tx.Rollback()
	stamp := now()
	result, err := tx.ExecContext(ctx, `UPDATE share_tokens
		SET use_count = use_count + 1, last_used_at = ?
		WHERE token_hash = ? AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > ?)
		  AND (max_uses IS NULL OR use_count < max_uses)`,
		stamp, tokenHash, stamp)
	if err != nil {
		return Share{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Share{}, sql.ErrNoRows
	}
	share, err := scanShare(tx.QueryRowContext(ctx, shareSelect+" WHERE token_hash = ?", tokenHash))
	if err != nil {
		return Share{}, err
	}
	if err := tx.Commit(); err != nil {
		return Share{}, err
	}
	return share, nil
}

func (db *DB) ValidateShare(ctx context.Context, tokenHash, workstationID string) (Share, error) {
	return scanShare(db.sql.QueryRowContext(ctx, shareSelect+
		` WHERE token_hash = ? AND workstation_id = ? AND revoked_at IS NULL
		   AND (expires_at IS NULL OR expires_at > ?)`,
		tokenHash, workstationID, now()))
}

func (db *DB) RevokeShare(ctx context.Context, workstationID string, shareID int64) error {
	result, err := db.sql.ExecContext(ctx, `UPDATE share_tokens SET revoked_at = ?
		WHERE id = ? AND workstation_id = ? AND revoked_at IS NULL`,
		now(), shareID, workstationID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (db *DB) shareByID(ctx context.Context, id int64) (Share, error) {
	return scanShare(db.sql.QueryRowContext(ctx, shareSelect+" WHERE id = ?", id))
}

const shareSelect = `SELECT id, workstation_id, permissions, named_recipient,
	COALESCE(expires_at, ''), max_uses, use_count, created_by, created_at,
	COALESCE(last_used_at, ''), COALESCE(revoked_at, '') FROM share_tokens`

func scanShare(row scanner) (Share, error) {
	var share Share
	var encoded, expires, created, lastUsed, revoked string
	var maxUses sql.NullInt64
	if err := row.Scan(&share.ID, &share.WorkstationID, &encoded, &share.NamedRecipient,
		&expires, &maxUses, &share.UseCount, &share.CreatedBy, &created, &lastUsed,
		&revoked); err != nil {
		return Share{}, err
	}
	permissions, err := sharing.Decode(encoded)
	if err != nil {
		return Share{}, fmt.Errorf("decode share permissions: %w", err)
	}
	share.Permissions = permissions
	share.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if parsed, err := time.Parse(time.RFC3339Nano, expires); err == nil {
		share.ExpiresAt = &parsed
	}
	if parsed, err := time.Parse(time.RFC3339Nano, lastUsed); err == nil {
		share.LastUsedAt = &parsed
	}
	if parsed, err := time.Parse(time.RFC3339Nano, revoked); err == nil {
		share.RevokedAt = &parsed
	}
	if maxUses.Valid {
		value := int(maxUses.Int64)
		share.MaxUses = &value
	}
	return share, nil
}

func (db *DB) CreateWorkstation(ctx context.Context, ws Workstation, apps []WorkstationApp) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := now()
	_, err = tx.ExecContext(ctx, `INSERT INTO workstations
		(id, name, owner_user_id, template_id, workspace_image, state, hostname,
		 cpu_limit, memory_limit_mb, pid_limit, persistent, vpn_required, vpn_profile_id,
		 created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ws.ID, ws.Name, ws.OwnerUserID, ws.TemplateID, ws.WorkspaceImage, ws.State,
		ws.Hostname, ws.CPULimit, ws.MemoryLimitMB, ws.PIDLimit, ws.Persistent,
		ws.VPNRequired, ws.VPNProfileID, stamp, stamp)
	if err != nil {
		return err
	}
	for _, app := range apps {
		_, err = tx.ExecContext(ctx, `INSERT INTO workstation_apps
			(workstation_id, app_id, app_version, state, internal_port, created_at, updated_at)
			VALUES(?, ?, ?, 'creating', ?, ?, ?)`,
			ws.ID, app.AppID, app.AppVersion, app.InternalPort, stamp, stamp)
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO events
		(workstation_id, event_type, message, created_at) VALUES(?, 'workstation.created', ?, ?)`,
		ws.ID, "Workstation creation requested", stamp); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) ListWorkstations(ctx context.Context, user User) ([]Workstation, error) {
	query := workstationSelect
	args := []any{}
	if !user.IsAdmin {
		query += " WHERE owner_user_id = ?"
		args = append(args, user.ID)
	}
	query += " ORDER BY created_at DESC"
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Workstation
	for rows.Next() {
		ws, err := scanWorkstation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, ws)
	}
	return result, rows.Err()
}

func (db *DB) Workstation(ctx context.Context, id string, user User) (Workstation, error) {
	query := workstationSelect + " WHERE id = ?"
	args := []any{id}
	if !user.IsAdmin {
		query += " AND owner_user_id = ?"
		args = append(args, user.ID)
	}
	ws, err := scanWorkstation(db.sql.QueryRowContext(ctx, query, args...))
	if err != nil {
		return Workstation{}, err
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT app_id, app_version, state, container_name, internal_port
		FROM workstation_apps WHERE workstation_id = ? ORDER BY app_id`, id)
	if err != nil {
		return Workstation{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var app WorkstationApp
		if err := rows.Scan(&app.AppID, &app.AppVersion, &app.State, &app.ContainerName, &app.InternalPort); err != nil {
			return Workstation{}, err
		}
		ws.Apps = append(ws.Apps, app)
	}
	return ws, rows.Err()
}

func (db *DB) WorkstationByHostname(ctx context.Context, hostname string, user User) (Workstation, error) {
	query := workstationSelect + " WHERE hostname = ?"
	args := []any{hostname}
	if !user.IsAdmin {
		query += " AND owner_user_id = ?"
		args = append(args, user.ID)
	}
	ws, err := scanWorkstation(db.sql.QueryRowContext(ctx, query, args...))
	if err != nil {
		return Workstation{}, err
	}
	return db.Workstation(ctx, ws.ID, user)
}

func (db *DB) SetWorkstationState(ctx context.Context, id, from, to, message string) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE workstations SET state = ?, error_message = ?,
		updated_at = ?, last_started_at = CASE WHEN ? = 'ready' THEN ? ELSE last_started_at END
		WHERE id = ? AND state = ?`, to, message, now(), to, now(), id, from)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return fmt.Errorf("workstation state changed concurrently (expected %s)", from)
	}
	eventType := "workstation.state." + to
	if _, err := tx.ExecContext(ctx, `INSERT INTO events
		(workstation_id, event_type, message, created_at) VALUES(?, ?, ?, ?)`,
		id, eventType, "State changed from "+from+" to "+to, now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) SetAppStates(ctx context.Context, id, state string) error {
	_, err := db.sql.ExecContext(ctx,
		"UPDATE workstation_apps SET state = ?, updated_at = ? WHERE workstation_id = ?",
		state, now(), id)
	return err
}

func (db *DB) SetAppVersion(ctx context.Context, workstationID, appID, version string) error {
	result, err := db.sql.ExecContext(ctx, `UPDATE workstation_apps
		SET app_version = ?, updated_at = ? WHERE workstation_id = ? AND app_id = ?`,
		version, now(), workstationID, appID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (db *DB) Events(ctx context.Context, workstationID string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT id, workstation_id, event_type, message, created_at
		FROM events WHERE workstation_id = ? ORDER BY id DESC LIMIT ?`, workstationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var event Event
		var created string
		if err := rows.Scan(&event.ID, &event.WorkstationID, &event.Type, &event.Message, &created); err != nil {
			return nil, err
		}
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (db *DB) RecordEvent(ctx context.Context, workstationID, eventType, message string) error {
	_, err := db.sql.ExecContext(ctx, `INSERT INTO events
		(workstation_id, event_type, message, created_at) VALUES(?, ?, ?, ?)`,
		workstationID, eventType, message, now())
	return err
}

func (db *DB) AllActiveWorkstations(ctx context.Context) ([]Workstation, error) {
	rows, err := db.sql.QueryContext(ctx, workstationSelect+" WHERE state NOT IN ('deleted', 'deleting')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Workstation
	for rows.Next() {
		ws, err := scanWorkstation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, ws)
	}
	return result, rows.Err()
}

const workstationSelect = `SELECT id, name, owner_user_id, template_id, workspace_image,
	state, hostname, cpu_limit, memory_limit_mb, pid_limit, persistent, vpn_required,
	vpn_profile_id, exit_ip, created_at, updated_at, COALESCE(last_started_at, ''),
	error_message FROM workstations`

type scanner interface {
	Scan(dest ...any) error
}

func scanWorkstation(row scanner) (Workstation, error) {
	var ws Workstation
	var created, updated, lastStarted string
	var vpnProfileID sql.NullInt64
	err := row.Scan(&ws.ID, &ws.Name, &ws.OwnerUserID, &ws.TemplateID, &ws.WorkspaceImage,
		&ws.State, &ws.Hostname, &ws.CPULimit, &ws.MemoryLimitMB, &ws.PIDLimit,
		&ws.Persistent, &ws.VPNRequired, &vpnProfileID, &ws.ExitIP, &created, &updated,
		&lastStarted, &ws.ErrorMessage)
	if err != nil {
		return Workstation{}, err
	}
	ws.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	ws.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	ws.LastStartedAt, _ = time.Parse(time.RFC3339Nano, lastStarted)
	if vpnProfileID.Valid {
		ws.VPNProfileID = &vpnProfileID.Int64
	}
	return ws, nil
}

func now() string                       { return formatTime(time.Now().UTC()) }
func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
