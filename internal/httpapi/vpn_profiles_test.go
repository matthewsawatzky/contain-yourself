package httpapi

import (
	"context"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workstation-manager/internal/config"
	"workstation-manager/internal/database"
	"workstation-manager/internal/workerclient"
)

func TestPrivateProfileIsEncryptedAndOwnerScoped(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := database.Open(ctx, filepath.Join(root, "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	admin, err := db.CreateInitialAdmin(ctx, "admin", "unused-hash")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := db.CreateUser(ctx, "alice", "unused-hash", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.CreateUser(ctx, "bob", "unused-hash", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Controller{
		AppsDirectory:          filepath.Join("..", "..", "core_apps"),
		InstalledAppsDirectory: filepath.Join(root, "apps"),
		AppStoreDirectory:      filepath.Join(root, "app-store"),
		AppStoreIndexURL:       "http://127.0.0.1/store/index.json",
		ControllerVersion:      "0.1.1",
		TemplatesDirectory:     filepath.Join("..", "..", "core_templates"),
		VPNProfilesDirectory:   filepath.Join(root, "vpn-profiles"),
		VPNEncryptionKeyFile:   filepath.Join(root, "vpn-profiles.key"),
		WorkerToken:            "abcdefghijklmnopqrstuvwxyz012345",
		SessionLifetime:        time.Hour,
	}
	server, err := New(cfg, db,
		workerclient.New("http://127.0.0.1:1", cfg.WorkerToken), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	secret := base64.StdEncoding.EncodeToString(make([]byte, 32))
	profile, err := server.storeVPNProfile(ctx, alice, vpnProfileInput{
		Name: "Personal London", Visibility: "global",
		WireGuardConfig: "[Interface]\nPrivateKey = " + secret +
			"\nAddress = 10.2.0.2/32\n\n[Peer]\nPublicKey = " + secret +
			"\nEndpoint = 1.2.3.4:51820\nAllowedIPs = 0.0.0.0/0\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Visibility != "private" || profile.OwnerUserID == nil ||
		*profile.OwnerUserID != alice.ID || profile.AutoAssign {
		t.Fatalf("normal-user profile scope = %#v", profile)
	}
	if _, err := db.VPNProfileForUser(ctx, profile.ID, bob); err == nil {
		t.Fatal("another user could access a private profile")
	}
	if _, err := db.VPNProfileForUser(ctx, profile.ID, admin); err == nil {
		t.Fatal("admin access bypassed profile catalogue rules")
	}
	ciphertext, err := os.ReadFile(filepath.Join(cfg.VPNProfilesDirectory, profile.ConfigRef))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), "PrivateKey") || strings.Contains(string(ciphertext), secret) {
		t.Fatal("encrypted profile contains plaintext WireGuard material")
	}
}
