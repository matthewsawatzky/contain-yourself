package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"workstation-manager/internal/auth"
	"workstation-manager/internal/database"
	"workstation-manager/internal/manifests"
	"workstation-manager/internal/sharing"
	"workstation-manager/internal/workstations"
)

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
		if s.shareCanOpenApp(share, app.AppID, http.MethodGet) {
			filtered = append(filtered, app)
		}
	}
	ws.Apps = filtered
	s.render(w, "launcher.html", pageData{
		Title: ws.Name, Workstation: ws, AppBase: "/shared/" + ws.ID,
		Shared: true, SharePermissions: share.Permissions,
		LauncherApps: s.launcherApps(ws),
	})
}

func (s *Server) proxyShared(w http.ResponseWriter, r *http.Request) {
	share := currentShare(r)
	appID := r.PathValue("app")
	if !s.shareCanOpenApp(share, appID, r.Method) {
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

// shareCanOpenApp resolves the permission an app requires from its manifest
// role rather than from a hard-coded app id, so a deployment can replace the
// bundled launcher or desktop without changing share authorization.
func (s *Server) shareCanOpenApp(share database.Share, appID, method string) bool {
	s.registryMu.RLock()
	app, known := s.apps.Get(appID)
	s.registryMu.RUnlock()
	if known && app.Desktop.Role == manifests.RoleLauncher {
		// The launcher only lists apps the share already grants access to.
		return true
	}
	switch appID {
	case "terminal":
		return sharing.Has(share.Permissions, sharing.TerminalControl)
	case "files":
		if method == http.MethodGet || method == http.MethodHead {
			return sharing.Has(share.Permissions, sharing.OpenApps) ||
				sharing.Has(share.Permissions, sharing.DownloadFiles)
		}
		return sharing.Has(share.Permissions, sharing.UploadFiles)
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
