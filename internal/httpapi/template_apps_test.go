package httpapi

import (
	"bytes"
	"context"
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

func TestInstalledAppCanBeAddedToBuiltInTemplate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := database.Open(ctx, filepath.Join(root, "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateInitialAdmin(ctx, "admin", "unused-hash")
	if err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(root, "apps", "browser")
	if err := os.MkdirAll(installed, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app.yaml", "icon.svg"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "app_store", "apps", "browser", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(installed, name), data, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Controller{
		AppsDirectory:          filepath.Join("..", "..", "core_apps"),
		InstalledAppsDirectory: filepath.Join(root, "apps"),
		AppStoreDirectory:      filepath.Join(root, "app-store"),
		AppStoreIndexURL:       "http://127.0.0.1/store/index.json",
		ControllerVersion:      "0.4.0",
		TemplatesDirectory:     filepath.Join("..", "..", "core_templates"),
		WorkerToken:            "abcdefghijklmnopqrstuvwxyz012345",
		SessionLifetime:        time.Hour,
	}
	server, err := New(cfg, db,
		workerclient.New("http://127.0.0.1:1", cfg.WorkerToken), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	server.registryMu.RLock()
	var rendered bytes.Buffer
	err = server.templates.ExecuteTemplate(&rendered, "templates.html", pageData{
		Title: "Templates", User: &user,
		Templates: server.presets.All(), Apps: server.apps.Entries(),
	})
	server.registryMu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.String(), "Template builder") {
		t.Fatal("administrator template builder did not render")
	}
	ws, err := server.create(ctx, user, createInput{
		Name: "Browser workspace", TemplateID: "terminal",
		Apps: []string{"web-desktop", "terminal", "browser"},
	})
	if err != nil {
		t.Fatalf("installed browser was rejected by built-in template: %v", err)
	}
	ws, err = db.Workstation(ctx, ws.ID, user)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, app := range ws.Apps {
		found = found || app.AppID == "browser"
	}
	if !found {
		t.Fatalf("created workstation apps = %#v", ws.Apps)
	}
}
