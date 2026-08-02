// Package httpapi implements the controller's HTML and JSON interfaces.
//
// The package is split by concern: authentication and sessions in auth.go,
// workstation lifecycle in workstations.go, share links in sharing.go, the app
// reverse proxy in proxy.go, the app store in store.go, administrative pages in
// settings.go, and the JSON client surface in api.go.
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"workstation-manager/internal/appstore"
	"workstation-manager/internal/config"
	"workstation-manager/internal/database"
	"workstation-manager/internal/egress"
	"workstation-manager/internal/manifests"
	"workstation-manager/internal/sharing"
	templatesregistry "workstation-manager/internal/templates"
	"workstation-manager/internal/theme"
	"workstation-manager/internal/workerclient"
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
	store      *appstore.Manager

	loginMu sync.Mutex
	logins  map[string]*loginAttempt
}

type pageData struct {
	Title                 string
	User                  *database.User
	Error                 string
	Notice                string
	Next                  string
	Workstations          []database.Workstation
	Workstation           database.Workstation
	Templates             []templatesregistry.Template
	Apps                  []manifests.Entry
	LauncherApps          []launcherApp
	Events                []database.Event
	AppBase               string
	Shares                []database.Share
	ShareURL              string
	Shared                bool
	SharePermissions      []sharing.Permission
	Usage                 []workerapi.ResourceUsage
	VPNProfiles           []database.VPNProfile
	Users                 []database.User
	StoreApps             []appstore.AppView
	StoreStatus           appstore.SyncStatus
	EgressModes           []egressChoice
	DefaultWorkspaceImage string
	Theme                 theme.Palette
	Presets               []theme.Preset
}

// egressChoice is one option in the template builder's connection menu.
type egressChoice struct {
	Value           string
	Label           string
	Description     string
	RequiresProfile bool
}

func egressChoices() []egressChoice {
	modes := egress.All()
	result := make([]egressChoice, 0, len(modes))
	for _, mode := range modes {
		result = append(result, egressChoice{
			Value: string(mode), Label: mode.Label(),
			Description: mode.Description(), RequiresProfile: mode.RequiresVPNProfile(),
		})
	}
	return result
}

type contextKey int

const userKey contextKey = 1
const shareKey contextKey = 2

func New(cfg config.Controller, db *database.DB, worker *workerclient.Client, logger *slog.Logger) (*Server, error) {
	server := &Server{logins: make(map[string]*loginAttempt)}
	parsed, err := template.New("ui").Funcs(template.FuncMap{
		"hasPermission": func(values []sharing.Permission, raw string) bool {
			return sharing.Has(values, sharing.Permission(raw))
		},
		// appRole and appName let templates branch on what an app is rather
		// than on a hard-coded app id, so a replacement launcher or desktop
		// renders correctly without template edits.
		"appRole": func(appID string) string {
			server.registryMu.RLock()
			defer server.registryMu.RUnlock()
			app, ok := server.apps.Get(appID)
			if !ok {
				return ""
			}
			return app.Desktop.Role
		},
		// egressLabel renders a template's connection mode, resolving the
		// legacy vpn_required flag for presets that predate named modes.
		"egressLabel": func(mode string, vpnRequired bool) string {
			return egress.Resolve(mode, vpnRequired).Label()
		},
		"appName": func(appID string) string {
			server.registryMu.RLock()
			defer server.registryMu.RUnlock()
			app, ok := server.apps.Get(appID)
			if !ok || app.Name == "" {
				return appID
			}
			return app.Name
		},
		"canManageVPN": func(profile database.VPNProfile, user *database.User) bool {
			return user != nil && (user.IsAdmin ||
				profile.OwnerUserID != nil && *profile.OwnerUserID == user.ID)
		},
	}).ParseFS(web.Assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse UI templates: %w", err)
	}
	server.config, server.db, server.worker = cfg, db, worker
	server.log, server.templates = logger, parsed
	coreRegistry, err := manifests.Scan(cfg.AppsDirectory)
	if err != nil {
		return nil, fmt.Errorf("scan core apps: %w", err)
	}
	var reservedIDs []string
	for _, entry := range coreRegistry.Entries() {
		if entry.Error == "" {
			reservedIDs = append(reservedIDs, entry.Manifest.ID)
		}
	}
	store, err := appstore.NewManager(appstore.ManagerConfig{
		RootDirectory:          cfg.AppStoreDirectory,
		InstalledAppsDirectory: cfg.InstalledAppsDirectory,
		IndexURL:               cfg.AppStoreIndexURL,
		ControllerVersion:      cfg.ControllerVersion,
		Approve:                server.approveStoreManifest,
		ReservedIDs:            reservedIDs,
		Platform:               runtime.GOOS + "/" + runtime.GOARCH,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize app store: %w", err)
	}
	server.store = store
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
	mux.HandleFunc("GET /theme.css", s.themeStylesheet)
	mux.HandleFunc("GET /setup", s.setupGet)
	mux.HandleFunc("POST /setup", s.setupPost)
	mux.HandleFunc("GET /login", s.loginGet)
	mux.HandleFunc("POST /login", s.loginPost)
	mux.Handle("POST /logout", s.requireUser(http.HandlerFunc(s.logout)))

	mux.Handle("GET /{$}", s.requireUser(http.HandlerFunc(s.root)))
	mux.Handle("POST /workstations", s.requireUser(http.HandlerFunc(s.createWorkstation)))
	mux.Handle("GET /templates", s.requireUser(http.HandlerFunc(s.templatesPage)))
	mux.Handle("POST /templates", s.requireAdmin(http.HandlerFunc(s.createCustomTemplate)))
	mux.Handle("POST /templates/{id}/delete", s.requireAdmin(http.HandlerFunc(s.deleteCustomTemplate)))
	mux.Handle("GET /appearance", s.requireUser(http.HandlerFunc(s.appearancePage)))
	mux.Handle("POST /appearance", s.requireUser(http.HandlerFunc(s.setUserAccent)))
	mux.Handle("POST /workstations/{id}/accent", s.requireUser(http.HandlerFunc(s.setWorkstationAccent)))
	mux.Handle("GET /users", s.requireAdmin(http.HandlerFunc(s.usersPage)))
	mux.Handle("POST /users", s.requireAdmin(http.HandlerFunc(s.createUser)))
	mux.Handle("GET /vpn-profiles", s.requireUser(http.HandlerFunc(s.vpnProfilesPage)))
	mux.Handle("POST /vpn-profiles", s.requireUser(http.HandlerFunc(s.createVPNProfile)))
	mux.Handle("POST /vpn-profiles/{id}/enabled", s.requireUser(http.HandlerFunc(s.setVPNProfileEnabled)))
	mux.Handle("GET /app-store", s.requireUser(http.HandlerFunc(s.appStorePage)))
	mux.Handle("POST /app-store/sync", s.requireAdmin(http.HandlerFunc(s.appStoreSync)))
	mux.Handle("POST /app-store/apps/{id}/install", s.requireAdmin(http.HandlerFunc(s.appStoreInstall)))
	mux.Handle("POST /app-store/apps/{id}/rollback", s.requireAdmin(http.HandlerFunc(s.appStoreRollback)))
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
	mux.HandleFunc("GET /api/v1/theme", s.apiTheme)
	mux.Handle("POST /api/v1/theme", s.requireUser(http.HandlerFunc(s.apiSetUserAccent)))
	mux.Handle("POST /api/v1/workstations/{id}/theme", s.requireUser(http.HandlerFunc(s.apiSetWorkstationAccent)))
	mux.Handle("GET /api/v1/apps", s.requireUser(http.HandlerFunc(s.apiApps)))
	mux.Handle("GET /api/v1/app-store", s.requireUser(http.HandlerFunc(s.apiAppStore)))
	mux.Handle("GET /api/v1/templates", s.requireUser(http.HandlerFunc(s.apiTemplates)))
	mux.Handle("GET /api/v1/vpn-profiles", s.requireUser(http.HandlerFunc(s.apiVPNProfiles)))
	mux.Handle("POST /api/v1/vpn-profiles", s.requireUser(http.HandlerFunc(s.apiCreateVPNProfile)))
	mux.Handle("POST /api/v1/vpn-profiles/{id}/enabled", s.requireUser(http.HandlerFunc(s.apiSetVPNProfileEnabled)))
	mux.Handle("GET /api/v1/users", s.requireAdmin(http.HandlerFunc(s.apiUsers)))
	mux.Handle("POST /api/v1/users", s.requireAdmin(http.HandlerFunc(s.apiCreateUser)))
	mux.Handle("POST /api/v1/admin/rescan", s.requireAdmin(http.HandlerFunc(s.apiRescan)))
	mux.Handle("POST /api/v1/admin/app-store/sync", s.requireAdmin(http.HandlerFunc(s.apiAppStoreSync)))
	mux.Handle("POST /api/v1/admin/app-store/apps/{id}/install", s.requireAdmin(http.HandlerFunc(s.apiAppStoreInstall)))
	mux.Handle("POST /api/v1/admin/app-store/apps/{id}/rollback", s.requireAdmin(http.HandlerFunc(s.apiAppStoreRollback)))
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
	appRegistry, err := manifests.ScanDirectories(
		s.config.AppsDirectory, s.config.InstalledAppsDirectory)
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

func parseOptionalID(value string) *int64 {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	return &id
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
