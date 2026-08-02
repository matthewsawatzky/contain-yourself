package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"workstation-manager/internal/config"
	"workstation-manager/internal/database"
	"workstation-manager/internal/sharing"
	"workstation-manager/internal/workerclient"
)

func launcherTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	db, err := database.Open(context.Background(), filepath.Join(root, "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := config.Controller{
		AppsDirectory:          filepath.Join("..", "..", "core_apps"),
		InstalledAppsDirectory: filepath.Join(root, "apps"),
		AppStoreDirectory:      filepath.Join(root, "app-store"),
		AppStoreIndexURL:       "http://127.0.0.1/store/index.json",
		ControllerVersion:      "0.4.1",
		TemplatesDirectory:     filepath.Join("..", "..", "core_templates"),
		WorkerToken:            "abcdefghijklmnopqrstuvwxyz012345",
		SessionLifetime:        time.Hour,
	}
	if err := os.MkdirAll(cfg.InstalledAppsDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	server, err := New(cfg, db,
		workerclient.New("http://127.0.0.1:1", cfg.WorkerToken), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return server
}

// The launcher must not list itself. Before desktop roles existed this was a
// hard-coded "web-desktop" comparison, which meant a replacement launcher
// would appear as a tile linking back to the page already on screen.
func TestLauncherAppsOmitsTheLauncherItself(t *testing.T) {
	server := launcherTestServer(t)
	ws := database.Workstation{
		ID: "ws-abc123def4",
		Apps: []database.WorkstationApp{
			{AppID: "web-desktop", State: "ready"},
			{AppID: "terminal", State: "ready"},
		},
	}
	tiles := server.launcherApps(ws)
	if len(tiles) != 1 {
		t.Fatalf("tiles = %+v, want only the terminal", tiles)
	}
	if tiles[0].ID != "terminal" {
		t.Fatalf("tile = %+v, want terminal", tiles[0])
	}
	if tiles[0].Name != "Terminal" || tiles[0].Initial != "T" {
		t.Fatalf("tile should carry the manifest name, got %+v", tiles[0])
	}
}

func TestLauncherAppsFallsBackToTheAppIDForUnknownApps(t *testing.T) {
	server := launcherTestServer(t)
	ws := database.Workstation{
		ID:   "ws-abc123def4",
		Apps: []database.WorkstationApp{{AppID: "uninstalled", State: "error"}},
	}
	tiles := server.launcherApps(ws)
	if len(tiles) != 1 || tiles[0].Name != "uninstalled" || tiles[0].Initial != "u" {
		t.Fatalf("tiles = %+v, want a fallback tile naming the app id", tiles)
	}
}

// A share that grants nothing must still reach the launcher, because the
// launcher only lists apps the share itself already permits.
func TestShareCanAlwaysOpenTheLauncher(t *testing.T) {
	server := launcherTestServer(t)
	empty := database.Share{Permissions: nil}
	if !server.shareCanOpenApp(empty, "web-desktop", http.MethodGet) {
		t.Fatal("launcher was denied to a share with no permissions")
	}
}

func TestShareAppPermissionsStillApplyPerApp(t *testing.T) {
	server := launcherTestServer(t)
	openApps := database.Share{Permissions: []sharing.Permission{sharing.OpenApps}}
	if server.shareCanOpenApp(openApps, "terminal", http.MethodGet) {
		t.Fatal("open-apps alone should not grant terminal control")
	}
	terminal := database.Share{Permissions: []sharing.Permission{sharing.TerminalControl}}
	if !server.shareCanOpenApp(terminal, "terminal", http.MethodGet) {
		t.Fatal("terminal-control did not grant the terminal")
	}
	if server.shareCanOpenApp(terminal, "code", http.MethodGet) {
		t.Fatal("terminal-control should not grant unrelated apps")
	}
}
