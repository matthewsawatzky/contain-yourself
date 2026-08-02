package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"workstation-manager/internal/auth"
	"workstation-manager/internal/database"
	"workstation-manager/internal/theme"
)

// themeFixture returns a server with one admin who owns one workstation, plus
// a session cookie for that admin.
func themeFixture(t *testing.T) (*Server, *database.DB, database.User, *http.Cookie) {
	t.Helper()
	server := launcherTestServer(t)
	ctx := context.Background()
	user, err := server.db.CreateInitialAdmin(ctx, "admin", "unused-hash")
	if err != nil {
		t.Fatal(err)
	}
	raw, hash, err := auth.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.db.CreateSession(ctx, user.ID, hash, timeSoon()); err != nil {
		t.Fatal(err)
	}
	ws := database.Workstation{
		ID: "ws-abc123def4", Name: "Research", OwnerUserID: user.ID,
		TemplateID: "terminal", WorkspaceImage: "alpine:3.21", State: "ready",
		Hostname: "ws-abc123def4", CPULimit: 2, MemoryLimitMB: 2048, PIDLimit: 512,
	}
	if err := server.db.CreateWorkstation(ctx, ws,
		[]database.WorkstationApp{{AppID: "terminal", InternalPort: 7681}}); err != nil {
		t.Fatal(err)
	}
	return server, server.db, user, &http.Cookie{Name: "wm_session", Value: raw}
}

func TestThemeStylesheetServesTheDefaultWithoutASession(t *testing.T) {
	server, _, _, _ := themeFixture(t)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/theme.css", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if kind := recorder.Header().Get("Content-Type"); !strings.HasPrefix(kind, "text/css") {
		t.Fatalf("content type = %q", kind)
	}
	if !strings.Contains(recorder.Body.String(), "--accent:"+theme.DefaultAccent) {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

// The stylesheet varies per viewer, so a shared cache must never reuse it.
func TestThemeStylesheetIsNotCacheable(t *testing.T) {
	server, _, _, _ := themeFixture(t)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/theme.css", nil))
	control := recorder.Header().Get("Cache-Control")
	if !strings.Contains(control, "private") || !strings.Contains(control, "no-store") {
		t.Fatalf("Cache-Control = %q", control)
	}
}

func TestThemeStylesheetFollowsTheSignedInUser(t *testing.T) {
	server, db, user, cookie := themeFixture(t)
	if err := db.SetUserAccent(context.Background(), user.ID, "#3ea6ff"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/theme.css", nil)
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if !strings.Contains(recorder.Body.String(), "--accent:#3ea6ff") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

func TestWorkstationAccentOverridesTheUserAccent(t *testing.T) {
	server, db, user, cookie := themeFixture(t)
	ctx := context.Background()
	if err := db.SetUserAccent(ctx, user.ID, "#3ea6ff"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetWorkstationAccent(ctx, "ws-abc123def4", user, "#22c55e"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/theme.css?workstation=ws-abc123def4", nil)
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if !strings.Contains(recorder.Body.String(), "--accent:#22c55e") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

// A workstation id in the query must not become a way to read another user's
// workstation record.
func TestThemeIgnoresAWorkstationTheViewerCannotSee(t *testing.T) {
	server, db, user, _ := themeFixture(t)
	ctx := context.Background()
	if err := db.SetWorkstationAccent(ctx, "ws-abc123def4", user, "#22c55e"); err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "intruder", "unused-hash", false)
	if err != nil {
		t.Fatal(err)
	}
	raw, hash, err := auth.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, other.ID, hash, timeSoon()); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/theme.css?workstation=ws-abc123def4", nil)
	request.AddCookie(&http.Cookie{Name: "wm_session", Value: raw})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	// Match the declaration, not the bare colour: the same value also appears
	// among the always-emitted preset swatch rules.
	if strings.Contains(recorder.Body.String(), "--accent:#22c55e") {
		t.Fatalf("another user's workstation accent leaked: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "--accent:"+theme.DefaultAccent) {
		t.Fatalf("expected the default accent instead: %s", recorder.Body.String())
	}
}

func TestAPIThemePublishesThePaletteAndPresets(t *testing.T) {
	server, db, user, cookie := themeFixture(t)
	if err := db.SetUserAccent(context.Background(), user.ID, "#ec4899"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/theme", nil)
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	var payload struct {
		Theme   theme.Palette  `json:"theme"`
		Presets []theme.Preset `json:"presets"`
		Default string         `json:"default"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	if payload.Theme.Accent != "#ec4899" || payload.Theme.Source != "user" {
		t.Fatalf("theme = %+v", payload.Theme)
	}
	if payload.Theme.OnAccent == "" || len(payload.Presets) == 0 ||
		payload.Default != theme.DefaultAccent {
		t.Fatalf("payload is incomplete: %+v", payload)
	}
}

func TestSetUserAccentRejectsNonHexValues(t *testing.T) {
	server, _, _, cookie := themeFixture(t)
	form := strings.NewReader("accent_color=" + urlEscape("#ff6b00;}body{display:none"))
	request := httptest.NewRequest(http.MethodPost, "/appearance", form)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", recorder.Code)
	}
}

func TestSetUserAccentStoresAndResets(t *testing.T) {
	server, db, _, cookie := themeFixture(t)
	post := func(value string) int {
		request := httptest.NewRequest(http.MethodPost, "/appearance",
			strings.NewReader("accent_color="+urlEscape(value)))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder.Code
	}
	if code := post("#22c55e"); code != http.StatusSeeOther {
		t.Fatalf("save status = %d", code)
	}
	stored, err := db.SessionUser(context.Background(), auth.TokenHash(cookie.Value))
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccentColor != "#22c55e" {
		t.Fatalf("stored accent = %q", stored.AccentColor)
	}
	if code := post("default"); code != http.StatusSeeOther {
		t.Fatalf("reset status = %d", code)
	}
	stored, _ = db.SessionUser(context.Background(), auth.TokenHash(cookie.Value))
	if stored.AccentColor != "" {
		t.Fatalf("reset left %q behind", stored.AccentColor)
	}
}

func TestWorkstationAccentCannotBeSetByANonOwner(t *testing.T) {
	_, db, _, _ := themeFixture(t)
	ctx := context.Background()
	other, err := db.CreateUser(ctx, "intruder", "unused-hash", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetWorkstationAccent(ctx, "ws-abc123def4", other, "#22c55e"); err == nil {
		t.Fatal("a non-owner was allowed to restyle another user's workstation")
	}
}

func TestAppearancePageRenders(t *testing.T) {
	server, _, _, cookie := themeFixture(t)
	request := httptest.NewRequest(http.MethodGet, "/appearance", nil)
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{"data-accent-form", "data-accent-input", "/theme.css"} {
		if !strings.Contains(body, want) {
			t.Errorf("appearance page is missing %q", want)
		}
	}
}

func timeSoon() time.Time { return time.Now().UTC().Add(time.Hour) }

func urlEscape(value string) string { return url.QueryEscape(value) }

// The palette is delivered as a stylesheet whose :root block has the same
// specificity as the static fallbacks in app.css, so it only takes effect if
// the browser loads it last. Getting this order wrong silently pins every page
// to the fallback colours.
func TestThemeStylesheetIsLinkedAfterTheStaticStylesheets(t *testing.T) {
	server, _, _, cookie := themeFixture(t)
	for _, path := range []string{"/", "/appearance", "/workstations/ws-abc123def4"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)

		body := recorder.Body.String()
		themeAt := strings.Index(body, "/theme.css")
		appAt := strings.LastIndex(body, "/static/app.css")
		if themeAt < 0 || appAt < 0 {
			t.Fatalf("%s is missing a stylesheet link (theme=%d app=%d)", path, themeAt, appAt)
		}
		if themeAt < appAt {
			t.Errorf("%s links /theme.css before app.css, so the accent is overridden", path)
		}
	}
}
