package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"workstation-manager/internal/auth"
	"workstation-manager/internal/database"
	"workstation-manager/internal/egress"
	templatesregistry "workstation-manager/internal/templates"
	"workstation-manager/internal/vpnprofiles"
)

func (s *Server) templatesPage(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	s.registryMu.RLock()
	data := pageData{
		Title: "Templates", User: &user,
		Templates: s.presets.All(), Apps: s.apps.Entries(),
		EgressModes: egressChoices(), DefaultWorkspaceImage: s.config.DefaultWorkspaceImage,
	}
	s.registryMu.RUnlock()
	s.render(w, "templates.html", data)
}

func (s *Server) createCustomTemplate(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, errors.New("invalid form"))
		return
	}
	id := strings.ToLower(strings.TrimSpace(r.FormValue("id")))
	id = strings.TrimPrefix(id, "custom-")
	cpu, cpuErr := strconv.ParseFloat(r.FormValue("cpu"), 64)
	memory, memoryErr := strconv.Atoi(r.FormValue("memory_mb"))
	pids, pidsErr := strconv.Atoi(r.FormValue("pid_limit"))
	expires := 0
	var expiresErr error
	if raw := strings.TrimSpace(r.FormValue("expires_hours")); raw != "" {
		expires, expiresErr = strconv.Atoi(raw)
	}
	if cpuErr != nil || memoryErr != nil || pidsErr != nil || expiresErr != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity,
			errors.New("CPU, memory, PID limit, and expiration must be valid numbers"))
		return
	}
	workspaceImage := strings.TrimSpace(r.FormValue("workspace_image"))
	if workspaceImage == "" {
		workspaceImage = s.config.DefaultWorkspaceImage
	}
	// The form submits one connection mode; vpn_required is derived from it so
	// the two can never be saved in disagreement.
	mode := strings.TrimSpace(r.FormValue("egress"))
	if mode == "" {
		mode = string(egress.Default)
	}
	selected, err := egress.Parse(mode)
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	value := templatesregistry.Template{
		SchemaVersion:  1,
		ID:             "custom-" + id,
		Name:           strings.TrimSpace(r.FormValue("name")),
		Description:    strings.TrimSpace(r.FormValue("description")),
		WorkspaceImage: workspaceImage,
		Apps:           unique(r.Form["apps"]),
		Egress:         mode,
		VPNRequired:    selected.RequiresVPNProfile(),
		Persistent:     r.FormValue("persistent") == "true",
		CPU:            cpu,
		MemoryMB:       memory,
		PIDLimit:       pids,
		ExpiresHours:   expires,
	}
	s.registryMu.RLock()
	err = templatesregistry.SaveCustom(s.config.TemplatesDirectory, value, func(id string) bool {
		_, ok := s.apps.Get(id)
		return ok
	})
	s.registryMu.RUnlock()
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	if err := s.rescan(); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	http.Redirect(w, r, "/templates", http.StatusSeeOther)
}

func (s *Server) deleteCustomTemplate(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	if err := templatesregistry.DeleteCustom(
		s.config.TemplatesDirectory, r.PathValue("id")); err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	if err := s.rescan(); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	http.Redirect(w, r, "/templates", http.StatusSeeOther)
}

func (s *Server) usersPage(w http.ResponseWriter, r *http.Request) {
	users, err := s.db.ListUsers(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	user := currentUser(r)
	s.render(w, "users.html", pageData{
		Title: "Users", User: &user, Users: users, EgressModes: grantableChoices(),
	})
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
	Username        string   `json:"username"`
	Password        string   `json:"password"`
	ConfirmPassword string   `json:"confirm_password"`
	IsAdmin         bool     `json:"is_admin"`
	AllowedEgress   []string `json:"allowed_egress,omitempty"`
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
	// An omitted grant set means "the usual", not "none": a caller that never
	// heard of egress grants should still create a usable account.
	grants := egress.DefaultGrants()
	if input.AllowedEgress != nil {
		grants, err = egress.ValidateGrants(input.AllowedEgress)
		if err != nil {
			return database.User{}, err
		}
	}
	return s.db.CreateUser(ctx, input.Username, hash, input.IsAdmin,
		egress.FormatGrants(grants))
}

func (s *Server) setUserEgress(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, errors.New("invalid form"))
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.renderError(w, r, http.StatusBadRequest, errors.New("invalid user id"))
		return
	}
	grants, err := egress.ValidateGrants(r.Form["allowed_egress"])
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	if err := s.db.SetUserEgress(r.Context(), id, egress.FormatGrants(grants)); err != nil {
		s.renderError(w, r, http.StatusNotFound, errors.New("user not found"))
		return
	}
	http.Redirect(w, r, "/users", http.StatusSeeOther)
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
	store := s.vpnStore()
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
	return s.vpnStore().Load(profile.ConfigRef)
}

// vpnStore builds the profile store from configuration. An operator-supplied
// key takes precedence over the generated key file.
func (s *Server) vpnStore() vpnprofiles.Store {
	return vpnprofiles.Store{
		Directory: s.config.VPNProfilesDirectory,
		KeyFile:   s.config.VPNEncryptionKeyFile,
		Key:       s.config.VPNEncryptionKey,
	}
}
