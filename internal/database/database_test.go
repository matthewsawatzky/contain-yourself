package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"workstation-manager/internal/sharing"
)

func TestMigrationsAdminSessionAndBackup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "controller.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database permissions = %o, want 600", info.Mode().Perm())
	}
	hasUsers, err := db.HasUsers(ctx)
	if err != nil || hasUsers {
		t.Fatalf("unexpected initial user state: users=%v err=%v", hasUsers, err)
	}
	user, err := db.CreateInitialAdmin(ctx, "admin", "test-hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateInitialAdmin(ctx, "other", "test-hash"); err == nil {
		t.Fatal("second initial administrator was accepted")
	}
	if err := db.CreateSession(ctx, user.ID, "token-hash", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SessionUser(ctx, "token-hash"); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeSession(ctx, "token-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SessionUser(ctx, "token-hash"); err == nil {
		t.Fatal("revoked session remained valid")
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := db.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	copy, err := Open(ctx, backup)
	if err != nil {
		t.Fatal(err)
	}
	copy.Close()
}

func TestShareRedemptionLimitsExpiryAndRevocation(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateInitialAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	ws := Workstation{
		ID: "ws-sharetest1", Name: "Shared", OwnerUserID: user.ID,
		TemplateID: "terminal", WorkspaceImage: "alpine:3.21", State: "ready",
		Hostname: "ws-sharetest1", CPULimit: 1, MemoryLimitMB: 512,
		PIDLimit: 128, Persistent: true,
	}
	if err := db.CreateWorkstation(ctx, ws, nil); err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(time.Hour)
	maxUses := 1
	created, err := db.CreateShare(ctx, ws.ID, user.ID, "one-use-token",
		[]sharing.Permission{sharing.OpenApps}, "Guest", &expiry, &maxUses)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RedeemShare(ctx, "one-use-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RedeemShare(ctx, "one-use-token"); err != sql.ErrNoRows {
		t.Fatalf("second redemption error = %v, want sql.ErrNoRows", err)
	}
	if _, err := db.ValidateShare(ctx, "one-use-token", ws.ID); err != nil {
		t.Fatalf("already-redeemed share cookie became invalid: %v", err)
	}
	if err := db.RevokeShare(ctx, ws.ID, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ValidateShare(ctx, "one-use-token", ws.ID); err != sql.ErrNoRows {
		t.Fatalf("revoked share validation error = %v, want sql.ErrNoRows", err)
	}
	past := time.Now().Add(-time.Minute)
	if _, err := db.CreateShare(ctx, ws.ID, user.ID, "expired-token",
		[]sharing.Permission{sharing.OpenApps}, "", &past, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RedeemShare(ctx, "expired-token"); err != sql.ErrNoRows {
		t.Fatalf("expired share redemption error = %v, want sql.ErrNoRows", err)
	}
}

func TestVPNProfileSelectionPersistsWithoutCredentials(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateInitialAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := db.CreateVPNProfile(ctx, VPNProfile{
		Name: "London", Provider: "custom", VPNType: "wireguard",
		Endpoint: "1.2.3.4:51820", Visibility: "global",
		ConfigRef: "0123456789abcdef0123456789abcdef.wg.enc", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ws := Workstation{
		ID: "ws-vpnprofile1", Name: "VPN workstation", OwnerUserID: user.ID,
		TemplateID: "developer", WorkspaceImage: "alpine:3.21", State: "creating",
		Hostname: "ws-vpnprofile1", CPULimit: 1, MemoryLimitMB: 512,
		PIDLimit: 128, VPNRequired: true, VPNProfileID: &profile.ID,
	}
	if err := db.CreateWorkstation(ctx, ws, nil); err != nil {
		t.Fatal(err)
	}
	stored, err := db.Workstation(ctx, ws.ID, user)
	if err != nil {
		t.Fatal(err)
	}
	if stored.VPNProfileID == nil || *stored.VPNProfileID != profile.ID {
		t.Fatalf("stored VPN profile = %v, want %d", stored.VPNProfileID, profile.ID)
	}
	if err := db.SetVPNProfileEnabled(ctx, profile.ID, user, false); err != nil {
		t.Fatal(err)
	}
	enabled, err := db.ListVPNProfiles(ctx, true)
	if err != nil || len(enabled) != 0 {
		t.Fatalf("enabled profiles = %v, err = %v", enabled, err)
	}
}

func TestVPNProfilesCombineGlobalAndPrivateCatalogues(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	admin, err := db.CreateInitialAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := db.CreateUser(ctx, "alice", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.CreateUser(ctx, "bob", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	global, err := db.CreateVPNProfile(ctx, VPNProfile{
		Name: "Company London", Provider: "custom", VPNType: "wireguard",
		Endpoint: "1.2.3.4:51820", Visibility: "global", AutoAssign: true,
		ConfigRef: "11111111111111111111111111111111.wg.enc", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	private, err := db.CreateVPNProfile(ctx, VPNProfile{
		Name: "Alice personal", Provider: "custom", VPNType: "wireguard",
		Endpoint: "5.6.7.8:51820", OwnerUserID: &alice.ID, Visibility: "private",
		ConfigRef: "22222222222222222222222222222222.wg.enc", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	aliceProfiles, err := db.ListVPNProfilesForUser(ctx, alice, true)
	if err != nil || len(aliceProfiles) != 2 {
		t.Fatalf("Alice profiles = %#v, err = %v", aliceProfiles, err)
	}
	bobProfiles, err := db.ListVPNProfilesForUser(ctx, bob, true)
	if err != nil || len(bobProfiles) != 1 || bobProfiles[0].ID != global.ID {
		t.Fatalf("Bob profiles = %#v, err = %v", bobProfiles, err)
	}
	if _, err := db.VPNProfileForUser(ctx, private.ID, bob); err != sql.ErrNoRows {
		t.Fatalf("Bob private-profile lookup error = %v, want sql.ErrNoRows", err)
	}
	if err := db.SetVPNProfileEnabled(ctx, private.ID, bob, false); err != sql.ErrNoRows {
		t.Fatalf("Bob disabled Alice's profile: %v", err)
	}
	if err := db.SetVPNProfileEnabled(ctx, private.ID, admin, false); err != nil {
		t.Fatalf("administrator could not disable private profile: %v", err)
	}
}
