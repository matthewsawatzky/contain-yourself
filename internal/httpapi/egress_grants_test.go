package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workstation-manager/internal/auth"
	"workstation-manager/internal/database"
	"workstation-manager/pkg/workerapi"
)

// grantFixture returns a server plus a non-admin user with the given grants,
// and a session cookie for that user.
func grantFixture(t *testing.T, grants string) (*Server, database.User, *http.Cookie) {
	t.Helper()
	server := launcherTestServer(t)
	ctx := context.Background()
	if _, err := server.db.CreateInitialAdmin(ctx, "admin", "unused-hash"); err != nil {
		t.Fatal(err)
	}
	user, err := server.db.CreateUser(ctx, "worker", "unused-hash", false, grants)
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
	return server, user, &http.Cookie{Name: "wm_session", Value: raw}
}

// The bundled "terminal" template is direct egress and "private" is wireguard,
// which gives both sides of the grant check without inventing templates.
func TestCreateAllowsAGrantedConnectionType(t *testing.T) {
	server, user, _ := grantFixture(t, "direct")
	if _, err := server.create(context.Background(), user, createInput{
		Name: "Allowed", TemplateID: "terminal",
	}); err != nil {
		t.Fatalf("a granted mode was refused: %v", err)
	}
}

func TestCreateRefusesAnUngrantedConnectionType(t *testing.T) {
	server, user, _ := grantFixture(t, "direct")
	_, err := server.create(context.Background(), user, createInput{
		Name: "Refused", TemplateID: "private",
	})
	if err == nil {
		t.Fatal("a wireguard template was created by a direct-only user")
	}
	if !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("error = %v, want a permission message", err)
	}
}

// Revoking every mode must stop workstation creation outright rather than
// falling back to a default.
func TestCreateRefusesEverythingWhenGrantsAreEmpty(t *testing.T) {
	server, user, _ := grantFixture(t, "")
	for _, template := range []string{"terminal", "private"} {
		if _, err := server.create(context.Background(), user, createInput{
			Name: "Nope", TemplateID: template,
		}); err == nil {
			t.Errorf("template %q was created with no grants", template)
		}
	}
}

func TestAdministratorsBypassGrants(t *testing.T) {
	server, _, _ := grantFixture(t, "")
	admin, err := server.db.CreateUser(context.Background(), "root2", "unused-hash", true, "")
	if err != nil {
		t.Fatal(err)
	}
	// "private" needs a VPN profile, which this fixture has none of. What
	// matters is that the refusal is not the grant check.
	_, err = server.create(context.Background(), admin, createInput{
		Name: "Admin box", TemplateID: "private",
	})
	if err != nil && strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("an administrator with no grants was blocked by the grant check: %v", err)
	}
}

// The grant check has to sit behind the JSON API too, not only the form, since
// a user who knows a template id could otherwise post around the UI.
func TestJSONAPIEnforcesGrants(t *testing.T) {
	server, _, cookie := grantFixture(t, "direct")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workstations",
		strings.NewReader(`{"name":"Bypass","template_id":"private"}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "not permitted") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

// host-gateway is administrator-only, so it must not be assignable even by an
// administrator editing another account.
func TestHostGatewayCannotBeGrantedThroughTheAPI(t *testing.T) {
	server, user, _ := grantFixture(t, "direct")
	ctx := context.Background()
	adminCookie := adminSession(t, server)

	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/users/"+itoa(user.ID)+"/egress",
		strings.NewReader(`{"allowed_egress":["direct","host-gateway"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(adminCookie)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", recorder.Code)
	}
	stored, err := server.db.SessionUser(ctx, auth.TokenHash(adminCookie.Value))
	if err != nil {
		t.Fatal(err)
	}
	_ = stored
}

func TestAdminCanChangeAUsersGrants(t *testing.T) {
	server, user, userCookie := grantFixture(t, "direct")
	adminCookie := adminSession(t, server)

	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/users/"+itoa(user.ID)+"/egress",
		strings.NewReader(`{"allowed_egress":["direct","wireguard"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(adminCookie)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}

	// The change must take effect for the user's next request.
	refreshed, err := server.db.SessionUser(context.Background(), auth.TokenHash(userCookie.Value))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(refreshed.AllowedEgress, "wireguard") {
		t.Fatalf("grants = %q, want wireguard added", refreshed.AllowedEgress)
	}
	_, err = server.create(context.Background(), refreshed, createInput{
		Name: "Now allowed", TemplateID: "private",
	})
	if err != nil && strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("newly granted mode is still blocked by the grant check: %v", err)
	}
}

// A non-admin must not be able to widen their own grants.
func TestNonAdminCannotChangeGrants(t *testing.T) {
	server, user, userCookie := grantFixture(t, "direct")
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/users/"+itoa(user.ID)+"/egress",
		strings.NewReader(`{"allowed_egress":["direct","wireguard"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(userCookie)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	refreshed, _ := server.db.SessionUser(context.Background(), auth.TokenHash(userCookie.Value))
	if strings.Contains(refreshed.AllowedEgress, "wireguard") {
		t.Fatal("a user widened their own grants")
	}
}

func TestNewUsersGetDefaultGrantsWhenUnspecified(t *testing.T) {
	server, _, _ := grantFixture(t, "direct")
	created, err := server.storeUser(context.Background(), userInput{
		Username: "fresh", Password: "correct-horse-battery",
		ConfirmPassword: "correct-horse-battery",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.AllowedEgress != "direct,wireguard" {
		t.Fatalf("default grants = %q", created.AllowedEgress)
	}
}

func adminSession(t *testing.T, server *Server) *http.Cookie {
	t.Helper()
	ctx := context.Background()
	admin, _, err := server.db.Authenticate(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	raw, hash, err := auth.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.db.CreateSession(ctx, admin.ID, hash, timeSoon()); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "wm_session", Value: raw}
}

func itoa(value int64) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// The healthy-tunnel panel cannot be reached without a live gateway, so the
// template is rendered directly against a populated status.
func TestWorkstationPageRendersALiveTunnel(t *testing.T) {
	server := launcherTestServer(t)
	var page bytes.Buffer
	err := server.templates.ExecuteTemplate(&page, "workstation.html", pageData{
		Title: "Research",
		Workstation: database.Workstation{
			ID: "ws-abc123def4", Name: "Research", State: "ready",
			EgressMode: "wireguard", VPNRequired: true,
		},
		EgressLabel: "VPN (WireGuard)",
		Egress: workerapi.EgressStatus{
			WorkstationID: "ws-abc123def4", Mode: "wireguard",
			Healthy: true, FailsClosed: true,
			Tunnel: &workerapi.TunnelStatus{
				Up: true, Endpoint: "203.0.113.10:51820",
				HandshakeAgeSeconds: 14, ReceivedBytes: 5 << 20, SentBytes: 2 << 20,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := page.String()
	for _, want := range []string{
		"VPN (WireGuard)", "203.0.113.10:51820", "14s ago",
		"5.0 MB", "2.0 MB", "Fails closed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("panel is missing %q", want)
		}
	}
	if strings.Contains(body, "Unavailable") {
		t.Error("a healthy tunnel was rendered as unavailable")
	}
}

func TestWorkstationPageRendersANeverHandshakedTunnel(t *testing.T) {
	server := launcherTestServer(t)
	var page bytes.Buffer
	err := server.templates.ExecuteTemplate(&page, "workstation.html", pageData{
		Title:       "Research",
		Workstation: database.Workstation{ID: "ws-abc123def4", Name: "Research"},
		EgressLabel: "VPN (WireGuard)",
		Egress: workerapi.EgressStatus{
			Mode: "wireguard", FailsClosed: true,
			Tunnel: &workerapi.TunnelStatus{Up: false, HandshakeAgeSeconds: -1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := page.String()
	// -1 must read as "never", not as a negative duration.
	if !strings.Contains(body, "never") {
		t.Error(`a tunnel that never handshaked should render "never"`)
	}
	if strings.Contains(body, "-1") {
		t.Error("the sentinel leaked into the page")
	}
}
