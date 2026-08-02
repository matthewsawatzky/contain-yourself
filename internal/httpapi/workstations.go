package httpapi

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"workstation-manager/internal/database"
	"workstation-manager/internal/egress"
	"workstation-manager/internal/manifests"
	"workstation-manager/internal/theme"
	"workstation-manager/internal/workstations"
	"workstation-manager/pkg/workerapi"
)

// launcherApp is one resolved tile on the workstation launcher. It carries the
// manifest name and role so the launcher does not have to know app ids.
type launcherApp struct {
	ID      string
	Name    string
	State   string
	Role    string
	Initial string
}

// launcherApps resolves the installed apps a launcher should offer. Apps that
// render the launcher itself are omitted so a replacement desktop does not
// list a link back to the page the user is already on.
func (s *Server) launcherApps(ws database.Workstation) []launcherApp {
	s.registryMu.RLock()
	defer s.registryMu.RUnlock()
	result := make([]launcherApp, 0, len(ws.Apps))
	for _, installed := range ws.Apps {
		app, known := s.apps.Get(installed.AppID)
		if known && app.Desktop.Role == manifests.RoleLauncher {
			continue
		}
		if known && !app.Desktop.Visible {
			continue
		}
		tile := launcherApp{
			ID: installed.AppID, Name: installed.AppID, State: installed.State,
		}
		if known {
			tile.Name = app.Name
			tile.Role = app.Desktop.Role
		}
		if tile.Name != "" {
			tile.Initial = tile.Name[:1]
		}
		result = append(result, tile)
	}
	return result
}

func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if hostname := s.workstationHostname(r.Host); hostname != "" {
		ws, err := s.db.WorkstationByHostname(r.Context(), hostname, user)
		if err != nil {
			s.renderError(w, r, http.StatusNotFound, errors.New("workstation not found"))
			return
		}
		s.render(w, "launcher.html", pageData{
			Title: ws.Name, User: &user, Workstation: ws,
			LauncherApps: s.launcherApps(ws),
		})
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
		Theme: s.resolveTheme(r, ws.AccentColor), Presets: theme.Presets,
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
		LauncherApps: s.launcherApps(ws),
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
}

type createInput struct {
	Name         string   `json:"name"`
	TemplateID   string   `json:"template_id"`
	Apps         []string `json:"apps"`
	VPNProfileID *int64   `json:"vpn_profile_id,omitempty"`
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
	var dbApps []database.WorkstationApp
	for _, id := range unique(input.Apps) {
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
	// Enforce the user's egress grants here, at the single point every creation
	// path funnels through, rather than by hiding templates in the UI. The JSON
	// API and the form both land here.
	mode := egress.Resolve(preset.Egress, preset.VPNRequired)
	if !egress.Granted(egress.ParseGrants(user.AllowedEgress), mode, user.IsAdmin) {
		return database.Workstation{}, fmt.Errorf(
			"your account is not permitted to use the %q connection type; ask an administrator",
			mode.Label())
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
		EgressMode:   string(mode),
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
	provisionMode := egress.Resolve(ws.EgressMode, ws.VPNRequired)
	request := workerapi.ProvisionRequest{
		WorkstationID: ws.ID, Persistent: ws.Persistent, VPNRequired: ws.VPNRequired,
		MemoryMB: ws.MemoryLimitMB, CPU: ws.CPULimit, PIDLimit: ws.PIDLimit,
		WorkspaceImage: ws.WorkspaceImage, EgressMode: string(provisionMode),
	}
	if provisionMode.RequiresVPNProfile() {
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
		entry, ok := s.apps.Entry(dbApp.AppID)
		if !ok {
			return request, fmt.Errorf("app %q disappeared from registry", dbApp.AppID)
		}
		app := entry.Manifest
		if app.Runtime.Type != "container-service" {
			continue
		}
		request.Apps = append(request.Apps, manifestAppSpec(app, entry.SHA256))
	}
	return request, nil
}

func manifestAppSpec(app manifests.Manifest, manifestSHA256 string) workerapi.AppSpec {
	spec := workerapi.AppSpec{
		ID: app.ID, Version: app.Version, ManifestSHA256: manifestSHA256,
		Image: app.Runtime.Image, Command: app.Runtime.Command,
		Environment: app.Runtime.Environment, InternalPort: app.Runtime.InternalPort,
		MemoryMB: app.Resources.DefaultMemoryMB, CPU: app.Resources.DefaultCPU,
		ShmSizeMB: app.Resources.ShmSizeMB, Capabilities: app.Permissions.Capabilities,
		HealthPath: app.Health.Path, HealthTimeoutSeconds: app.Health.TimeoutSeconds,
	}
	for _, storage := range app.Storage {
		spec.Storage = append(spec.Storage, workerapi.StorageSpec{
			Type: storage.Type, Target: storage.Target,
			OwnerUID: storage.OwnerUID, OwnerGID: storage.OwnerGID,
		})
	}
	return spec
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
