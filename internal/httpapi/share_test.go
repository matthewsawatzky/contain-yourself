package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workstation-manager/internal/auth"
	"workstation-manager/internal/config"
	"workstation-manager/internal/database"
	"workstation-manager/internal/sharing"
	"workstation-manager/internal/workerclient"
)

func TestShareRedemptionPermissionLimitAndRevocation(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateInitialAdmin(ctx, "admin", "unused-hash")
	if err != nil {
		t.Fatal(err)
	}
	ws := database.Workstation{
		ID: "ws-sharehttp1", Name: "Shared terminal", OwnerUserID: user.ID,
		TemplateID: "terminal", WorkspaceImage: "alpine:3.21", State: "ready",
		Hostname: "ws-sharehttp1", CPULimit: 1, MemoryLimitMB: 512,
		PIDLimit: 128, Persistent: true,
	}
	apps := []database.WorkstationApp{
		{AppID: "web-desktop", AppVersion: "1.0.0"},
		{AppID: "terminal", AppVersion: "1.0.0", InternalPort: 7681},
	}
	if err := db.CreateWorkstation(ctx, ws, apps); err != nil {
		t.Fatal(err)
	}
	raw := "share-secret-with-more-than-thirty-two-characters"
	expiry := time.Now().Add(time.Hour)
	oneUse := 1
	share, err := db.CreateShare(ctx, ws.ID, user.ID, auth.TokenHash(raw),
		[]sharing.Permission{sharing.OpenApps}, "", &expiry, &oneUse)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Controller{
		AppsDirectory:      filepath.Join("..", "..", "apps"),
		TemplatesDirectory: filepath.Join("..", "..", "templates"),
		WorkerToken:        "abcdefghijklmnopqrstuvwxyz012345",
		SessionLifetime:    time.Hour,
	}
	server, err := New(cfg, db, workerclient.New("http://127.0.0.1:1", cfg.WorkerToken), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	redeem := httptest.NewRecorder()
	handler.ServeHTTP(redeem, httptest.NewRequest(http.MethodGet, "/share/"+raw, nil))
	if redeem.Code != http.StatusSeeOther {
		t.Fatalf("redeem status = %d body=%s", redeem.Code, redeem.Body.String())
	}
	var shareCookie *http.Cookie
	for _, cookie := range redeem.Result().Cookies() {
		if cookie.Name == "wm_share" {
			shareCookie = cookie
		}
	}
	if shareCookie == nil {
		t.Fatal("redemption did not issue share cookie")
	}
	launcherRequest := httptest.NewRequest(http.MethodGet, "/shared/"+ws.ID, nil)
	launcherRequest.AddCookie(shareCookie)
	launcher := httptest.NewRecorder()
	handler.ServeHTTP(launcher, launcherRequest)
	if launcher.Code != http.StatusOK {
		t.Fatalf("shared launcher status = %d body=%s", launcher.Code, launcher.Body.String())
	}
	if strings.Contains(launcher.Body.String(), `/shared/`+ws.ID+`/apps/terminal/`) {
		t.Fatal("open-apps share unexpectedly exposed terminal")
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/share/"+raw, nil))
	if second.Code != http.StatusNotFound {
		t.Fatalf("second redemption status = %d, want 404", second.Code)
	}
	terminalRequest := httptest.NewRequest(http.MethodGet, "/shared/"+ws.ID+"/apps/terminal/", nil)
	terminalRequest.AddCookie(shareCookie)
	terminal := httptest.NewRecorder()
	handler.ServeHTTP(terminal, terminalRequest)
	if terminal.Code != http.StatusForbidden {
		t.Fatalf("terminal status = %d, want 403", terminal.Code)
	}
	if err := db.RevokeShare(ctx, ws.ID, share.ID); err != nil {
		t.Fatal(err)
	}
	revokedRequest := httptest.NewRequest(http.MethodGet, "/shared/"+ws.ID, nil)
	revokedRequest.AddCookie(shareCookie)
	revoked := httptest.NewRecorder()
	handler.ServeHTTP(revoked, revokedRequest)
	if revoked.Code != http.StatusForbidden {
		t.Fatalf("revoked share status = %d, want 403", revoked.Code)
	}
}
