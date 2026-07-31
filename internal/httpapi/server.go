// Package httpapi implements the controller's HTML and JSON interfaces.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"workstation-manager/internal/auth"
	"workstation-manager/internal/config"
	"workstation-manager/internal/database"
	"workstation-manager/internal/manifests"
	"workstation-manager/internal/proxy"
	"workstation-manager/internal/sharing"
	templatesregistry "workstation-manager/internal/templates"
	"workstation-manager/internal/vpnprofiles"
	"workstation-manager/internal/workerclient"
	"workstation-manager/internal/workstations"
	"workstation-manager/pkg/workerapi"
	"workstation-manager/web"
)

type Server struct {
	config    config.Controller
	db        *database.DB
	worker    *workerclient.Client
	log       *slog.Logger
	templates *template.Template

	registryMu sync.RWMutex
	apps       *manifests.Registry
	presets    *templatesregistry.Registry

	loginMu sync.Mutex
	logins  map[string]*loginAttempt
}

type loginAttempt struct {
	Failures int
	Blocked  time.Time
	LastSeen time.Time
}

type pageData struct {
	Title            string
	User             *database.User
	Error            string
	Notice           string
	Next             string
	Workstations     []database.Workstation
	Workstation      database.Workstation
	Templates        []templatesregistry.Template
	Apps             []manifests.Entry
	Events           []database.Event
	AppBase          string
	Shares           []database.Share
	ShareURL         string
	Shared           bool
	SharePermissions []sharing.Permission
	Usage            []workerapi.ResourceUsage
	VPNProfiles      []database.VPNProfile
	Users            []database.User
}

type contextKey int

const userKey contextKey = 1
const shareKey contextKey = 2

func New(cfg config.Controller, db *database.DB, worker *workerclient.Client, logger *slog.Logger) (*Server, error) {
	parsed, err := template.New("ui").Funcs(template.FuncMap{
		"hasPermission": func(values []sharing.Permission, raw string) bool {
			return sharing.Has(values, sharing.Permission(raw))
		},
		"canManageVPN": func(profile database.VPNProfile, user *database.User) bool {
			return user != nil && (user.IsAdmin ||
				profile.OwnerUserID != nil && *profile.OwnerUserID == user.ID)
		},
	}).ParseFS(web.Assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse UI templates: %w", err)
	}
	server := &Server{
		config: cfg, db: db, worker: worker, log: logger, templates: parsed,
		logins: make(map[string]*loginAttempt),
	}
	if err := server.rescan(); err != nil {
		return nil, err
	}
	return server, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	staticFS, _ := fs.Sub(web.Assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /setup", s.setupGet)
	mux.HandleFunc("POST /setup", s.setupPost)
	mux.HandleFunc("GET /login", s.loginGet)
	mux.HandleFunc("POST /login", s.loginPost)
	mux.Handle("POST /logout", s.requireUser(http.HandlerFunc(s.logout)))

	mux.Handle("GET /{$}", s.requireUser(http.HandlerFunc(s.root)))
	mux.Handle("POST /workstations", s.requireUser(http.HandlerFunc(s.createWorkstation)))
	mux.Handle("GET /users", s.requireAdmin(http.HandlerFunc(s.usersPage)))
	mux.Handle("POST /users", s.requireAdmin(http.HandlerFunc(s.createUser)))
	mux.Handle("GET /vpn-profiles", s.requireUser(http.HandlerFunc(s.vpnProfilesPage)))
	mux.Handle("POST /vpn-profiles", s.requireUser(http.HandlerFunc(s.createVPNProfile)))
	mux.Handle("POST /vpn-profiles/{id}/enabled", s.requireUser(http.HandlerFunc(s.setVPNProfileEnabled)))
	mux.Handle("GET /workstations/{id}", s.requireUser(http.HandlerFunc(s.workstationPage)))
	mux.Handle("GET /workstations/{id}/desktop", s.requireUser(http.HandlerFunc(s.desktopPage)))
	mux.Handle("POST /workstations/{id}/actions/{action}", s.requireUser(http.HandlerFunc(s.workstationAction)))
	mux.Handle("POST /workstations/{id}/shares", s.requireUser(http.HandlerFunc(s.createShare)))
	mux.Handle("POST /workstations/{id}/shares/{share}/revoke", s.requireUser(http.HandlerFunc(s.revokeShare)))
	mux.Handle("/workstations/{id}/apps/{app}/", s.requireUser(http.HandlerFunc(s.proxyExplicit)))
	mux.Handle("/apps/{app}/", s.requireUser(http.HandlerFunc(s.proxyHostname)))
	mux.HandleFunc("GET /share/{token}", s.redeemShare)
	mux.Handle("GET /shared/{id}", s.requireShare(http.HandlerFunc(s.sharedLauncher)))
	mux.Handle("/shared/{id}/apps/{app}/", s.requireShare(http.HandlerFunc(s.proxyShared)))
	mux.Handle("POST /shared/{id}/actions/{action}", s.requireShare(http.HandlerFunc(s.sharedAction)))
	mux.Handle("POST /shared/{id}/logout", s.requireShare(http.HandlerFunc(s.sharedLogout)))

	mux.Handle("GET /api/v1/status", s.requireUser(http.HandlerFunc(s.apiStatus)))
	mux.Handle("GET /api/v1/workstations", s.requireUser(http.HandlerFunc(s.apiWorkstations)))
	mux.Handle("POST /api/v1/workstations", s.requireUser(http.HandlerFunc(s.apiCreateWorkstation)))
	mux.Handle("GET /api/v1/workstations/{id}", s.requireUser(http.HandlerFunc(s.apiWorkstation)))
	mux.Handle("POST /api/v1/workstations/{id}/actions/{action}", s.requireUser(http.HandlerFunc(s.apiWorkstationAction)))
	mux.Handle("GET /api/v1/workstations/{id}/usage", s.requireUser(http.HandlerFunc(s.apiUsage)))
	mux.Handle("GET /api/v1/workstations/{id}/apps/{app}/logs", s.requireUser(http.HandlerFunc(s.apiLogs)))
	mux.Handle("GET /api/v1/workstations/{id}/shares", s.requireUser(http.HandlerFunc(s.apiShares)))
	mux.Handle("POST /api/v1/workstations/{id}/shares", s.requireUser(http.HandlerFunc(s.apiCreateShare)))
	mux.Handle("POST /api/v1/workstations/{id}/shares/{share}/revoke", s.requireUser(http.HandlerFunc(s.apiRevokeShare)))
	mux.Handle("GET /api/v1/apps", s.requireUser(http.HandlerFunc(s.apiApps)))
	mux.Handle("GET /api/v1/templates", s.requireUser(http.HandlerFunc(s.apiTemplates)))
	mux.Handle("GET /api/v1/vpn-profiles", s.requireUser(http.HandlerFunc(s.apiVPNProfiles)))
	mux.Handle("POST /api/v1/vpn-profiles", s.requireUser(http.HandlerFunc(s.apiCreateVPNProfile)))
	mux.Handle("POST /api/v1/vpn-profiles/{id}/enabled", s.requireUser(http.HandlerFunc(s.apiSetVPNProfileEnabled)))
	mux.Handle("GET /api/v1/users", s.requireAdmin(http.HandlerFunc(s.apiUsers)))
	mux.Handle("POST /api/v1/users", s.requireAdmin(http.HandlerFunc(s.apiCreateUser)))
	mux.Handle("POST /api/v1/admin/rescan", s.requireAdmin(http.HandlerFunc(s.apiRescan)))
	mux.Handle("POST /api/v1/admin/reconcile", s.requireAdmin(http.HandlerFunc(s.apiReconcile)))
	mux.Handle("POST /api/v1/admin/backup", s.requireAdmin(http.HandlerFunc(s.apiBackup)))
	return securityHeaders(requestLogger(s.log, mux))
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.IntegrityCheck(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unready", "database": err.Error()})
		return
	}
	if err := s.worker.Health(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unready", "worker": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) setupGet(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.db.HasUsers(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	if hasUsers {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, "setup.html", pageData{Title: "Setup"})
}

func (s *Server) setupPost(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.render(w, "setup.html", pageData{Title: "Setup", Error: "Invalid form submission."})
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if len(username) < 3 || len(username) > 64 || strings.ContainsAny(username, " \t\r\n") {
		s.render(w, "setup.html", pageData{Title: "Setup", Error: "Username must be 3–64 characters without spaces."})
		return
	}
	if password != r.FormValue("confirm_password") {
		s.render(w, "setup.html", pageData{Title: "Setup", Error: "Passwords do not match."})
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.render(w, "setup.html", pageData{Title: "Setup", Error: err.Error()})
		return
	}
	user, err := s.db.CreateInitialAdmin(r.Context(), username, hash)
	if err != nil {
		s.render(w, "setup.html", pageData{Title: "Setup", Error: err.Error()})
		return
	}
	if err := s.startSession(w, r, user.ID); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.log.Info("initial administrator created", "user_id", user.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginGet(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.db.HasUsers(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	if !hasUsers {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	next := safeNext(r.URL.Query().Get("next"))
	s.render(w, "login.html", pageData{Title: "Login", Next: next})
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	ip := remoteIP(r)
	if wait := s.loginBlocked(ip); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		s.renderStatus(w, "login.html", http.StatusTooManyRequests,
			pageData{Title: "Login", Error: "Too many failed attempts. Try again shortly."})
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	user, passwordHash, err := s.db.Authenticate(r.Context(), strings.TrimSpace(r.FormValue("username")))
	if err != nil || !auth.VerifyPassword(passwordHash, r.FormValue("password")) {
		s.recordLogin(ip, false)
		time.Sleep(250 * time.Millisecond)
		s.renderStatus(w, "login.html", http.StatusUnauthorized,
			pageData{Title: "Login", Error: "Invalid username or password.", Next: safeNext(r.FormValue("next"))})
		return
	}
	s.recordLogin(ip, true)
	if err := s.startSession(w, r, user.ID); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	if cookie, err := r.Cookie("wm_session"); err == nil {
		_ = s.db.RevokeSession(r.Context(), auth.TokenHash(cookie.Value))
	}
	s.clearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if hostname := s.workstationHostname(r.Host); hostname != "" {
		ws, err := s.db.WorkstationByHostname(r.Context(), hostname, user)
		if err != nil {
			s.renderError(w, r, http.StatusNotFound, errors.New("workstation not found"))
			return
		}
		s.render(w, "launcher.html", pageData{Title: ws.Name, User: &user, Workstation: ws})
		return
	}
	workstationList, err := s.db.ListWorkstations(r.Context(), user)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.registryMu.RLock()
	profiles, _ := s.db.ListVPNProfilesForUser(r.Context(), user, true)
	data := pageData{
		Title: "Dashboard", User: &user, Workstations: workstationList,
		Templates: s.presets.All(), Apps: s.apps.Entries(), VPNProfiles: profiles,
	}
	s.registryMu.RUnlock()
	s.render(w, "dashboard.html", data)
}

func (s *Server) createWorkstation(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ws, err := s.create(r.Context(), currentUser(r), createInput{
		Name: r.FormValue("name"), TemplateID: r.FormValue("template_id"),
		Apps: r.Form["apps"], VPNProfileID: parseOptionalID(r.FormValue("vpn_profile_id")),
	})
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	http.Redirect(w, r, "/workstations/"+ws.ID, http.StatusSeeOther)
}

func (s *Server) usersPage(w http.ResponseWriter, r *http.Request) {
	users, err := s.db.ListUsers(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	user := currentUser(r)
	s.render(w, "users.html", pageData{Title: "Users", User: &user, Users: users})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, errors.New("invalid user form"))
		return
	}
	_, err := s.storeUser(r.Context(), userInput{
		Username: r.FormValue("username"), Password: r.FormValue("password"),
		ConfirmPassword: r.FormValue("confirm_password"),
		IsAdmin:         r.FormValue("is_admin") == "true",
	})
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

type userInput struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
	IsAdmin         bool   `json:"is_admin"`
}

func (s *Server) storeUser(ctx context.Context, input userInput) (database.User, error) {
	input.Username = strings.TrimSpace(input.Username)
	if len(input.Username) < 3 || len(input.Username) > 64 ||
		strings.ContainsAny(input.Username, " \t\r\n") {
		return database.User{}, errors.New("username must be 3–64 characters without spaces")
	}
	if input.Password != input.ConfirmPassword {
		return database.User{}, errors.New("passwords do not match")
	}
	hash, err := auth.HashPassword(input.Password)
	if err != nil {
		return database.User{}, err
	}
	return s.db.CreateUser(ctx, input.Username, hash, input.IsAdmin)
}

func (s *Server) vpnProfilesPage(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var profiles []database.VPNProfile
	var err error
	if user.IsAdmin {
		profiles, err = s.db.ListVPNProfiles(r.Context(), false)
	} else {
		profiles, err = s.db.ListVPNProfilesForUser(r.Context(), user, false)
	}
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.render(w, "vpn-profiles.html", pageData{
		Title: "VPN profiles", User: &user, VPNProfiles: profiles,
	})
}

func (s *Server) createVPNProfile(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, errors.New("invalid VPN profile form"))
		return
	}
	_, err := s.storeVPNProfile(r.Context(), currentUser(r), vpnProfileInput{
		Name: r.FormValue("name"), Visibility: r.FormValue("visibility"),
		AutoAssign:      r.FormValue("auto_assign") == "true",
		WireGuardConfig: r.FormValue("wireguard_config"),
	})
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	http.Redirect(w, r, "/vpn-profiles", http.StatusSeeOther)
}

func (s *Server) setVPNProfileEnabled(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, errors.New("invalid VPN profile id"))
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, errors.New("invalid form"))
		return
	}
	enabled := r.FormValue("enabled") == "true"
	if err := s.db.SetVPNProfileEnabled(r.Context(), id, currentUser(r), enabled); err != nil {
		s.renderError(w, r, http.StatusNotFound, errors.New("VPN profile not found"))
		return
	}
	http.Redirect(w, r, "/vpn-profiles", http.StatusSeeOther)
}

func (s *Server) workstationPage(w http.ResponseWriter, r *http.Request) {
	ws, err := s.db.Workstation(r.Context(), r.PathValue("id"), currentUser(r))
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, errors.New("workstation not found"))
		return
	}
	s.renderWorkstation(w, r, ws, "")
}

func (s *Server) renderWorkstation(w http.ResponseWriter, r *http.Request, ws database.Workstation, shareURL string) {
	events, _ := s.db.Events(r.Context(), ws.ID, 100)
	shares, _ := s.db.ListShares(r.Context(), ws.ID)
	usageContext, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	usage, _ := s.worker.Usage(usageContext, ws.ID)
	cancel()
	user := currentUser(r)
	s.render(w, "workstation.html", pageData{
		Title: ws.Name, User: &user, Workstation: ws, Events: events,
		Shares: shares, ShareURL: shareURL, Usage: usage.Resources,
	})
}

func (s *Server) desktopPage(w http.ResponseWriter, r *http.Request) {
	ws, err := s.db.Workstation(r.Context(), r.PathValue("id"), currentUser(r))
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, errors.New("workstation not found"))
		return
	}
	user := currentUser(r)
	s.render(w, "launcher.html", pageData{
		Title: ws.Name, User: &user, Workstation: ws, AppBase: "/workstations/" + ws.ID,
	})
}

func (s *Server) workstationAction(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	id, action := r.PathValue("id"), r.PathValue("action")
	if err := s.beginAction(r.Context(), currentUser(r), id, action); err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
	if action != "delete" {
		// Browsers get a stable destination while the asynchronous action runs.
	}
}

func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, errors.New("invalid share form"))
		return
	}
	user := currentUser(r)
	ws, err := s.db.Workstation(r.Context(), r.PathValue("id"), user)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, errors.New("workstation not found"))
		return
	}
	permissions, expiresAt, maxUses, recipient, err := parseShareInput(
		r.Form["permissions"], r.FormValue("expires_hours"),
		r.FormValue("max_uses"), r.FormValue("recipient"))
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	raw, hash, err := auth.RandomToken(32)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	if _, err := s.db.CreateShare(r.Context(), ws.ID, user.ID, hash, permissions,
		recipient, expiresAt, maxUses); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	_ = s.db.RecordEvent(r.Context(), ws.ID, "share.created",
		"Workstation share created for "+shareRecipient(recipient))
	s.renderWorkstation(w, r, ws, "/share/"+raw)
}

func (s *Server) revokeShare(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	ws, err := s.db.Workstation(r.Context(), r.PathValue("id"), user)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, errors.New("workstation not found"))
		return
	}
	shareID, err := strconv.ParseInt(r.PathValue("share"), 10, 64)
	if err != nil || shareID <= 0 {
		s.renderError(w, r, http.StatusBadRequest, errors.New("invalid share id"))
		return
	}
	if err := s.db.RevokeShare(r.Context(), ws.ID, shareID); err != nil {
		s.renderError(w, r, http.StatusNotFound, errors.New("share not found or already revoked"))
		return
	}
	_ = s.db.RecordEvent(r.Context(), ws.ID, "share.revoked", "Workstation share revoked")
	http.Redirect(w, r, "/workstations/"+ws.ID, http.StatusSeeOther)
}

func (s *Server) redeemShare(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if len(token) < 32 || len(token) > 128 {
		s.renderError(w, r, http.StatusNotFound, errors.New("share is invalid or unavailable"))
		return
	}
	share, err := s.db.RedeemShare(r.Context(), auth.TokenHash(token))
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, errors.New("share is invalid, expired, revoked, or fully used"))
		return
	}
	maxAge := 0
	var expires time.Time
	if share.ExpiresAt != nil {
		expires = *share.ExpiresAt
		maxAge = max(1, int(time.Until(expires).Seconds()))
	}
	http.SetCookie(w, &http.Cookie{
		Name: "wm_share", Value: token, Path: "/shared/" + share.WorkstationID,
		Expires: expires, MaxAge: maxAge, HttpOnly: true, Secure: s.config.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	_ = s.db.RecordEvent(r.Context(), share.WorkstationID, "share.redeemed", "Workstation share redeemed")
	http.Redirect(w, r, "/shared/"+share.WorkstationID, http.StatusSeeOther)
}

func (s *Server) requireShare(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workstationID := r.PathValue("id")
		cookie, err := r.Cookie("wm_share")
		if err != nil {
			s.renderError(w, r, http.StatusUnauthorized, errors.New("share authentication required"))
			return
		}
		share, err := s.db.ValidateShare(r.Context(), auth.TokenHash(cookie.Value), workstationID)
		if err != nil {
			s.clearShare(w, workstationID)
			s.renderError(w, r, http.StatusForbidden, errors.New("share is expired or revoked"))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead &&
			r.Method != http.MethodOptions && !sameOrigin(r) {
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), shareKey, share)))
	})
}

func (s *Server) sharedLauncher(w http.ResponseWriter, r *http.Request) {
	share := currentShare(r)
	ws, err := s.db.Workstation(r.Context(), share.WorkstationID, database.User{IsAdmin: true})
	if err != nil || ws.State == string(workstations.StateDeleted) {
		s.renderError(w, r, http.StatusNotFound, errors.New("workstation is unavailable"))
		return
	}
	filtered := ws.Apps[:0]
	for _, app := range ws.Apps {
		if app.AppID == "web-desktop" || shareCanOpenApp(share, app.AppID, http.MethodGet) {
			filtered = append(filtered, app)
		}
	}
	ws.Apps = filtered
	s.render(w, "launcher.html", pageData{
		Title: ws.Name, Workstation: ws, AppBase: "/shared/" + ws.ID,
		Shared: true, SharePermissions: share.Permissions,
	})
}

func (s *Server) proxyShared(w http.ResponseWriter, r *http.Request) {
	share := currentShare(r)
	appID := r.PathValue("app")
	if !shareCanOpenApp(share, appID, r.Method) {
		http.Error(w, "share permission denied", http.StatusForbidden)
		return
	}
	ws, err := s.db.Workstation(r.Context(), share.WorkstationID, database.User{IsAdmin: true})
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.proxyApp(w, r, ws, appID, "/shared/"+ws.ID)
}

func (s *Server) sharedAction(w http.ResponseWriter, r *http.Request) {
	share := currentShare(r)
	action := r.PathValue("action")
	required := map[string]sharing.Permission{
		"restart": sharing.RestartWorkstation,
		"stop":    sharing.StopWorkstation,
	}[action]
	if required == "" || !sharing.Has(share.Permissions, required) {
		http.Error(w, "share permission denied", http.StatusForbidden)
		return
	}
	if err := s.beginAction(r.Context(), database.User{IsAdmin: true},
		share.WorkstationID, action); err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	http.Redirect(w, r, "/shared/"+share.WorkstationID, http.StatusSeeOther)
}

func (s *Server) sharedLogout(w http.ResponseWriter, r *http.Request) {
	share := currentShare(r)
	s.clearShare(w, share.WorkstationID)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) clearShare(w http.ResponseWriter, workstationID string) {
	http.SetCookie(w, &http.Cookie{
		Name: "wm_share", Value: "", Path: "/shared/" + workstationID,
		MaxAge: -1, HttpOnly: true, Secure: s.config.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

func currentShare(r *http.Request) database.Share {
	share, _ := r.Context().Value(shareKey).(database.Share)
	return share
}

func shareCanOpenApp(share database.Share, appID, method string) bool {
	switch appID {
	case "terminal":
		return sharing.Has(share.Permissions, sharing.TerminalControl)
	case "files":
		if method == http.MethodGet || method == http.MethodHead {
			return sharing.Has(share.Permissions, sharing.OpenApps) ||
				sharing.Has(share.Permissions, sharing.DownloadFiles)
		}
		return sharing.Has(share.Permissions, sharing.UploadFiles)
	case "web-desktop":
		return true
	default:
		return sharing.Has(share.Permissions, sharing.OpenApps)
	}
}

func parseShareInput(rawPermissions []string, expiresRaw, maxUsesRaw, recipient string) (
	[]sharing.Permission, *time.Time, *int, string, error) {
	permissions, err := sharing.Validate(rawPermissions)
	if err != nil {
		return nil, nil, nil, "", err
	}
	var expiresAt *time.Time
	if expiresRaw != "" && expiresRaw != "0" {
		hours, err := strconv.Atoi(expiresRaw)
		if err != nil || hours < 1 || hours > 8760 {
			return nil, nil, nil, "", errors.New("expiration must be between 1 and 8760 hours")
		}
		value := time.Now().UTC().Add(time.Duration(hours) * time.Hour)
		expiresAt = &value
	}
	var maxUses *int
	if maxUsesRaw != "" && maxUsesRaw != "0" {
		uses, err := strconv.Atoi(maxUsesRaw)
		if err != nil || uses < 1 || uses > 10000 {
			return nil, nil, nil, "", errors.New("maximum uses must be between 1 and 10000")
		}
		maxUses = &uses
	}
	recipient = strings.TrimSpace(recipient)
	if len(recipient) > 128 {
		return nil, nil, nil, "", errors.New("recipient name is too long")
	}
	return permissions, expiresAt, maxUses, recipient, nil
}

func shareRecipient(recipient string) string {
	if recipient == "" {
		return "an unnamed recipient"
	}
	return recipient
}

type createInput struct {
	Name         string   `json:"name"`
	TemplateID   string   `json:"template_id"`
	Apps         []string `json:"apps"`
	VPNProfileID *int64   `json:"vpn_profile_id,omitempty"`
}

type vpnProfileInput struct {
	Name            string `json:"name"`
	Visibility      string `json:"visibility"`
	AutoAssign      bool   `json:"auto_assign"`
	WireGuardConfig string `json:"wireguard_config"`
}

func (s *Server) storeVPNProfile(ctx context.Context, user database.User,
	input vpnProfileInput) (database.VPNProfile, error) {
	input.Name = strings.TrimSpace(input.Name)
	if len(input.Name) < 2 || len(input.Name) > 80 {
		return database.VPNProfile{}, errors.New("profile name must contain 2–80 characters")
	}
	input.Visibility = strings.ToLower(strings.TrimSpace(input.Visibility))
	if !user.IsAdmin {
		input.Visibility = "private"
	}
	if input.Visibility == "" {
		input.Visibility = "private"
	}
	if input.Visibility != "private" && input.Visibility != "global" {
		return database.VPNProfile{}, errors.New("visibility must be private or global")
	}
	if input.AutoAssign && (!user.IsAdmin || input.Visibility != "global") {
		return database.VPNProfile{}, errors.New("only administrators can recommend global profiles")
	}
	parsed, err := vpnprofiles.Parse(input.WireGuardConfig)
	if err != nil {
		return database.VPNProfile{}, err
	}
	store := vpnprofiles.Store{
		Directory: s.config.VPNProfilesDirectory,
		KeyFile:   s.config.VPNEncryptionKeyFile,
	}
	ref, err := store.Save(parsed.Canonical)
	if err != nil {
		return database.VPNProfile{}, err
	}
	profile := database.VPNProfile{
		Name: input.Name, Provider: "custom", VPNType: "wireguard",
		Endpoint: parsed.Endpoint, Visibility: input.Visibility,
		AutoAssign: input.AutoAssign, ConfigRef: ref, Enabled: true,
	}
	if input.Visibility == "private" {
		profile.OwnerUserID = &user.ID
	}
	created, err := s.db.CreateVPNProfile(ctx, profile)
	if err != nil {
		store.Remove(ref)
		return database.VPNProfile{}, err
	}
	return created, nil
}

func (s *Server) loadVPNConfig(profile database.VPNProfile) (string, error) {
	return (vpnprofiles.Store{
		Directory: s.config.VPNProfilesDirectory,
		KeyFile:   s.config.VPNEncryptionKeyFile,
	}).Load(profile.ConfigRef)
}

func parseOptionalID(value string) *int64 {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	return &id
}

func (s *Server) create(ctx context.Context, user database.User, input createInput) (database.Workstation, error) {
	input.Name = strings.TrimSpace(input.Name)
	if len(input.Name) < 2 || len(input.Name) > 80 {
		return database.Workstation{}, errors.New("name must contain 2–80 characters")
	}
	s.registryMu.RLock()
	preset, ok := s.presets.Get(input.TemplateID)
	if !ok {
		s.registryMu.RUnlock()
		return database.Workstation{}, errors.New("unknown template")
	}
	if len(input.Apps) == 0 {
		input.Apps = append([]string(nil), preset.Apps...)
	}
	allowed := make(map[string]bool)
	for _, id := range preset.Apps {
		allowed[id] = true
	}
	var dbApps []database.WorkstationApp
	for _, id := range unique(input.Apps) {
		if !allowed[id] {
			s.registryMu.RUnlock()
			return database.Workstation{}, fmt.Errorf("app %q is not allowed by template %q", id, preset.ID)
		}
		app, exists := s.apps.Get(id)
		if !exists {
			s.registryMu.RUnlock()
			return database.Workstation{}, fmt.Errorf("app %q is unavailable", id)
		}
		dbApps = append(dbApps, database.WorkstationApp{
			AppID: app.ID, AppVersion: app.Version, InternalPort: app.Runtime.InternalPort,
		})
	}
	s.registryMu.RUnlock()
	if len(dbApps) == 0 {
		return database.Workstation{}, errors.New("at least one app is required")
	}
	var vpnProfileID *int64
	if preset.VPNRequired {
		if input.VPNProfileID == nil || *input.VPNProfileID <= 0 {
			return database.Workstation{}, errors.New("this template requires an enabled VPN profile")
		}
		profile, err := s.db.VPNProfileForUser(ctx, *input.VPNProfileID, user)
		if err != nil || !profile.Enabled {
			return database.Workstation{}, errors.New("selected VPN profile is unavailable")
		}
		vpnProfileID = &profile.ID
	}
	id, err := workstationID()
	if err != nil {
		return database.Workstation{}, err
	}
	ws := database.Workstation{
		ID: id, Name: input.Name, OwnerUserID: user.ID, TemplateID: preset.ID,
		WorkspaceImage: preset.WorkspaceImage, State: string(workstations.StateCreating),
		Hostname: id, CPULimit: preset.CPU, MemoryLimitMB: preset.MemoryMB,
		PIDLimit: preset.PIDLimit, Persistent: preset.Persistent, VPNRequired: preset.VPNRequired,
		VPNProfileID: vpnProfileID,
	}
	if err := s.db.CreateWorkstation(ctx, ws, dbApps); err != nil {
		return database.Workstation{}, err
	}
	go s.provision(ws.ID, user)
	return ws, nil
}

func (s *Server) provision(id string, user database.User) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	ws, err := s.db.Workstation(ctx, id, user)
	if err != nil {
		return
	}
	if err := s.transition(ctx, &ws, workstations.StatePullingImages, ""); err != nil {
		return
	}
	request, err := s.provisionRequest(ctx, ws)
	if err == nil {
		_, err = s.worker.Provision(ctx, request)
	}
	if err != nil {
		s.log.Error("workstation provisioning failed", "workstation_id", id, "error", err)
		_ = s.transition(ctx, &ws, workstations.StateError, err.Error())
		_ = s.db.SetAppStates(ctx, id, "error")
		return
	}
	if err := s.transition(ctx, &ws, workstations.StateCreatingStorage, ""); err != nil {
		return
	}
	if ws.VPNRequired {
		for _, state := range []workstations.State{
			workstations.StateStartingVPN, workstations.StateWaitingForVPN,
		} {
			if err := s.transition(ctx, &ws, state, ""); err != nil {
				return
			}
		}
	}
	if err := s.transition(ctx, &ws, workstations.StateStartingApps, ""); err != nil {
		return
	}
	s.registryMu.RLock()
	for _, app := range ws.Apps {
		if manifest, ok := s.apps.Get(app.AppID); ok {
			_ = s.db.SetAppVersion(ctx, id, app.AppID, manifest.Version)
		}
	}
	s.registryMu.RUnlock()
	_ = s.db.SetAppStates(ctx, id, "ready")
	_ = s.transition(ctx, &ws, workstations.StateReady, "")
}

func (s *Server) provisionRequest(ctx context.Context, ws database.Workstation) (workerapi.ProvisionRequest, error) {
	request := workerapi.ProvisionRequest{
		WorkstationID: ws.ID, Persistent: ws.Persistent, VPNRequired: ws.VPNRequired,
		MemoryMB: ws.MemoryLimitMB, CPU: ws.CPULimit, PIDLimit: ws.PIDLimit,
	}
	if ws.VPNRequired {
		if ws.VPNProfileID == nil {
			return request, errors.New("VPN workstation has no selected profile")
		}
		profile, err := s.db.VPNProfile(ctx, *ws.VPNProfileID)
		if err != nil {
			return request, fmt.Errorf("load VPN profile: %w", err)
		}
		if !profile.Enabled {
			return request, errors.New("selected VPN profile is disabled")
		}
		config, err := s.loadVPNConfig(profile)
		if err != nil {
			return request, fmt.Errorf("load WireGuard configuration: %w", err)
		}
		request.VPNProfile = &workerapi.VPNProfile{
			WireGuardConfig: config,
		}
	}
	s.registryMu.RLock()
	defer s.registryMu.RUnlock()
	for _, dbApp := range ws.Apps {
		app, ok := s.apps.Get(dbApp.AppID)
		if !ok {
			return request, fmt.Errorf("app %q disappeared from registry", dbApp.AppID)
		}
		if app.Runtime.Type != "container-service" {
			continue
		}
		spec := workerapi.AppSpec{
			ID: app.ID, Image: app.Runtime.Image, Command: app.Runtime.Command,
			Environment:  app.Runtime.Environment,
			InternalPort: app.Runtime.InternalPort, MemoryMB: app.Resources.DefaultMemoryMB,
			CPU: app.Resources.DefaultCPU, ShmSizeMB: app.Resources.ShmSizeMB,
			Capabilities: app.Permissions.Capabilities,
			HealthPath:   app.Health.Path, HealthTimeoutSeconds: app.Health.TimeoutSeconds,
		}
		for _, storage := range app.Storage {
			spec.Storage = append(spec.Storage, workerapi.StorageSpec{
				Type: storage.Type, Target: storage.Target,
				OwnerUID: storage.OwnerUID, OwnerGID: storage.OwnerGID,
			})
		}
		request.Apps = append(request.Apps, spec)
	}
	return request, nil
}

func (s *Server) beginAction(ctx context.Context, user database.User, id, action string) error {
	ws, err := s.db.Workstation(ctx, id, user)
	if err != nil {
		return errors.New("workstation not found")
	}
	switch action {
	case "stop":
		if err := s.transition(ctx, &ws, workstations.StateStopping, ""); err != nil {
			return err
		}
		go s.runAction(id, user, "stop", workstations.StateStopped)
	case "start":
		var next = workstations.StateStartingApps
		if ws.VPNRequired {
			next = workstations.StateStartingVPN
		}
		if err := s.transition(ctx, &ws, next, ""); err != nil {
			return err
		}
		go s.runAction(id, user, "start", workstations.StateReady)
	case "restart":
		if err := s.transition(ctx, &ws, workstations.StateStopping, ""); err != nil {
			return err
		}
		go s.runRestart(id, user)
	case "update":
		if err := s.transition(ctx, &ws, workstations.StatePullingImages, ""); err != nil {
			return err
		}
		go s.runUpdate(id, user)
	case "delete":
		if err := s.transition(ctx, &ws, workstations.StateDeleting, ""); err != nil {
			return err
		}
		go s.runAction(id, user, "delete", workstations.StateDeleted)
	default:
		return errors.New("unknown workstation action")
	}
	return nil
}

func (s *Server) runUpdate(id string, user database.User) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	ws, err := s.db.Workstation(ctx, id, user)
	if err != nil {
		return
	}
	request, err := s.provisionRequest(ctx, ws)
	if err == nil {
		_, err = s.worker.Rebuild(ctx, request)
	}
	if err != nil {
		_ = s.transition(ctx, &ws, workstations.StateError, err.Error())
		_ = s.db.SetAppStates(ctx, id, "error")
		return
	}
	if err := s.transition(ctx, &ws, workstations.StateCreatingStorage, ""); err != nil {
		return
	}
	if ws.VPNRequired {
		if err := s.transition(ctx, &ws, workstations.StateStartingVPN, ""); err != nil {
			return
		}
		if err := s.transition(ctx, &ws, workstations.StateWaitingForVPN, ""); err != nil {
			return
		}
	}
	if err := s.transition(ctx, &ws, workstations.StateStartingApps, ""); err != nil {
		return
	}
	s.registryMu.RLock()
	for _, app := range ws.Apps {
		if manifest, ok := s.apps.Get(app.AppID); ok {
			_ = s.db.SetAppVersion(ctx, id, app.AppID, manifest.Version)
		}
	}
	s.registryMu.RUnlock()
	_ = s.db.SetAppStates(ctx, id, "ready")
	_ = s.transition(ctx, &ws, workstations.StateReady, "")
}

func (s *Server) runAction(id string, user database.User, action string, final workstations.State) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	ws, err := s.db.Workstation(ctx, id, user)
	if err != nil {
		return
	}
	if err := s.worker.Action(ctx, id, action); err != nil {
		_ = s.transition(ctx, &ws, workstations.StateError, err.Error())
		return
	}
	if action == "start" && ws.VPNRequired {
		if err := s.transition(ctx, &ws, workstations.StateWaitingForVPN, ""); err != nil {
			return
		}
		if err := s.transition(ctx, &ws, workstations.StateStartingApps, ""); err != nil {
			return
		}
	}
	_ = s.db.SetAppStates(ctx, id, map[string]string{
		"start": "ready", "stop": "stopped", "delete": "deleted",
	}[action])
	_ = s.transition(ctx, &ws, final, "")
}

func (s *Server) runRestart(id string, user database.User) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	ws, err := s.db.Workstation(ctx, id, user)
	if err != nil {
		return
	}
	if err := s.worker.Action(ctx, id, "restart"); err != nil {
		_ = s.transition(ctx, &ws, workstations.StateError, err.Error())
		return
	}
	if err := s.transition(ctx, &ws, workstations.StateStopped, ""); err != nil {
		return
	}
	next := workstations.StateStartingApps
	if ws.VPNRequired {
		next = workstations.StateStartingVPN
	}
	if err := s.transition(ctx, &ws, next, ""); err != nil {
		return
	}
	if ws.VPNRequired {
		if err := s.transition(ctx, &ws, workstations.StateWaitingForVPN, ""); err != nil {
			return
		}
		if err := s.transition(ctx, &ws, workstations.StateStartingApps, ""); err != nil {
			return
		}
	}
	_ = s.db.SetAppStates(ctx, id, "ready")
	_ = s.transition(ctx, &ws, workstations.StateReady, "")
}

func (s *Server) transition(ctx context.Context, ws *database.Workstation, next workstations.State, message string) error {
	current := workstations.State(ws.State)
	if err := workstations.ValidateTransition(current, next); err != nil {
		return err
	}
	if err := s.db.SetWorkstationState(ctx, ws.ID, ws.State, string(next), message); err != nil {
		return err
	}
	ws.State = string(next)
	ws.ErrorMessage = message
	return nil
}

func (s *Server) proxyExplicit(w http.ResponseWriter, r *http.Request) {
	ws, err := s.db.Workstation(r.Context(), r.PathValue("id"), currentUser(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	appID := r.PathValue("app")
	http.SetCookie(w, &http.Cookie{
		Name: "wm_active_" + appID, Value: ws.ID, Path: "/apps/" + appID + "/",
		HttpOnly: true, Secure: s.config.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
	s.registryMu.RLock()
	app, ok := s.apps.Get(appID)
	s.registryMu.RUnlock()
	if ok && !app.Routing.StripPrefix {
		destination := strings.TrimPrefix(r.URL.Path, "/workstations/"+ws.ID)
		if r.URL.RawQuery != "" {
			destination += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, destination, http.StatusTemporaryRedirect)
		return
	}
	s.proxyApp(w, r, ws, appID, "/workstations/"+ws.ID)
}

func (s *Server) proxyHostname(w http.ResponseWriter, r *http.Request) {
	hostname := s.workstationHostname(r.Host)
	if hostname == "" {
		if cookie, err := r.Cookie("wm_active_" + r.PathValue("app")); err == nil {
			hostname = cookie.Value
		}
		if hostname == "" {
			http.NotFound(w, r)
			return
		}
	}
	ws, err := s.db.WorkstationByHostname(r.Context(), hostname, currentUser(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.proxyApp(w, r, ws, r.PathValue("app"), "")
}

func (s *Server) proxyApp(w http.ResponseWriter, r *http.Request, ws database.Workstation, appID, explicitPrefix string) {
	if ws.State != string(workstations.StateReady) {
		http.Error(w, "workstation is not ready", http.StatusServiceUnavailable)
		return
	}
	var installed bool
	for _, app := range ws.Apps {
		installed = installed || app.AppID == appID
	}
	if !installed {
		http.NotFound(w, r)
		return
	}
	s.registryMu.RLock()
	app, ok := s.apps.Get(appID)
	s.registryMu.RUnlock()
	if !ok || app.Runtime.Type != "container-service" {
		http.NotFound(w, r)
		return
	}
	host := "wm-" + ws.ID + "-app-" + appID
	if ws.VPNRequired {
		host = "wm-" + ws.ID + "-vpn"
	}
	target, _ := url.Parse(fmt.Sprintf("http://%s:%d", host, app.Runtime.InternalPort))
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(request *http.Request) {
		original(request)
		if explicitPrefix != "" {
			request.URL.Path = strings.TrimPrefix(request.URL.Path, explicitPrefix)
		}
		if app.Routing.StripPrefix {
			request.URL.Path = strings.TrimPrefix(request.URL.Path, "/apps/"+appID)
			if request.URL.Path == "" {
				request.URL.Path = "/"
			}
		}
		request.Header.Set("X-Workstation-ID", ws.ID)
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		if explicitPrefix != "" {
			if location := response.Header.Get("Location"); strings.HasPrefix(location, "/") &&
				!strings.HasPrefix(location, explicitPrefix+"/") {
				response.Header.Set("Location", explicitPrefix+location)
			}
		}
		cookies := response.Header.Values("Set-Cookie")
		response.Header.Del("Set-Cookie")
		for _, cookie := range cookies {
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(cookie)), "wm_session=") {
				response.Header.Add("Set-Cookie", cookie)
			}
		}
		return nil
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, request *http.Request, err error) {
		s.log.Warn("app proxy failed", "workstation_id", ws.ID, "app_id", appID, "error", err)
		http.Error(rw, "application is unavailable", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("wm_session")
		if err != nil {
			s.redirectLogin(w, r)
			return
		}
		user, err := s.db.SessionUser(r.Context(), auth.TokenHash(cookie.Value))
		if err != nil {
			s.clearSession(w)
			s.redirectLogin(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead &&
			r.Method != http.MethodOptions && !sameOrigin(r) {
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return s.requireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).IsAdmin {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "administrator access required"})
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	raw, hash, err := auth.RandomToken(32)
	if err != nil {
		return err
	}
	expires := time.Now().UTC().Add(s.config.SessionLifetime)
	if err := s.db.CreateSession(r.Context(), userID, hash, expires); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: "wm_session", Value: raw, Path: "/", Expires: expires,
		MaxAge: int(s.config.SessionLifetime.Seconds()), HttpOnly: true,
		Secure: s.config.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: "wm_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: s.config.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) render(w http.ResponseWriter, name string, data pageData) {
	s.renderStatus(w, name, http.StatusOK, data)
}

func (s *Server) renderStatus(w http.ResponseWriter, name string, status int, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		s.log.Error("render template", "template", name, "error", err)
	}
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, err error) {
	title := http.StatusText(status)
	data := pageData{Title: title, Error: err.Error()}
	if user := currentUserPointer(r); user != nil {
		data.User = user
	}
	s.renderStatus(w, "error.html", status, data)
}

func (s *Server) rescan() error {
	appRegistry, err := manifests.Scan(s.config.AppsDirectory)
	if err != nil {
		return err
	}
	presets, err := templatesregistry.Scan(s.config.TemplatesDirectory, func(id string) bool {
		_, ok := appRegistry.Get(id)
		return ok
	})
	if err != nil {
		return err
	}
	s.registryMu.Lock()
	s.apps, s.presets = appRegistry, presets
	s.registryMu.Unlock()
	return nil
}

func (s *Server) Reconcile(ctx context.Context) error {
	resources, err := s.worker.List(ctx)
	if err != nil {
		return err
	}
	byWorkstation := make(map[string][]workerapi.Resource)
	for _, resource := range resources {
		byWorkstation[resource.WorkstationID] = append(byWorkstation[resource.WorkstationID], resource)
	}
	records, err := s.db.AllActiveWorkstations(ctx)
	if err != nil {
		return err
	}
	for _, ws := range records {
		items := byWorkstation[ws.ID]
		if len(items) == 0 && ws.State != string(workstations.StateCreating) &&
			ws.State != string(workstations.StatePullingImages) {
			_ = s.db.SetWorkstationState(ctx, ws.ID, ws.State, string(workstations.StateError),
				"Reconciliation found no managed Docker resources")
		}
		delete(byWorkstation, ws.ID)
	}
	for id := range byWorkstation {
		s.log.Warn("orphaned managed resources detected; not deleting", "workstation_id", id)
	}
	return nil
}

func (s *Server) workstationHostname(hostport string) string {
	return proxy.WorkstationHostname(hostport, s.config.PublicBaseDomain)
}

func (s *Server) redirectLogin(w http.ResponseWriter, r *http.Request) {
	next := r.URL.RequestURI()
	http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
}

func (s *Server) loginBlocked(ip string) time.Duration {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	attempt := s.logins[ip]
	if attempt == nil {
		return 0
	}
	return time.Until(attempt.Blocked)
}

func (s *Server) recordLogin(ip string, success bool) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if success {
		delete(s.logins, ip)
		return
	}
	attempt := s.logins[ip]
	if attempt == nil {
		attempt = &loginAttempt{}
		s.logins[ip] = attempt
	}
	attempt.Failures++
	attempt.LastSeen = time.Now()
	if attempt.Failures >= 5 {
		attempt.Blocked = time.Now().Add(time.Duration(attempt.Failures-4) * 30 * time.Second)
	}
	for key, value := range s.logins {
		if time.Since(value.LastSeen) > 24*time.Hour {
			delete(s.logins, key)
		}
	}
}

func currentUser(r *http.Request) database.User {
	user, _ := r.Context().Value(userKey).(database.User)
	return user
}

func currentUserPointer(r *http.Request) *database.User {
	user, ok := r.Context().Value(userKey).(database.User)
	if !ok {
		return nil
	}
	return &user
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		// API clients authenticate with a host-only session cookie and are still
		// constrained by SameSite=Lax.
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, r.Host)
}

func safeNext(value string) string {
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return "/"
	}
	return value
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func workstationID() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	data := make([]byte, 10)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	for i := range data {
		data[i] = alphabet[int(data[i])%len(alphabet)]
	}
	return "ws-" + string(data), nil
}

func unique(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "same-origin")
		if !strings.Contains(r.URL.Path, "/apps/") {
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; frame-src 'self'; connect-src 'self' ws: wss:")
		}
		next.ServeHTTP(w, r)
	})
}

func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Info("controller request", "method", r.Method, "path", r.URL.Path, "remote", remoteIP(r))
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
