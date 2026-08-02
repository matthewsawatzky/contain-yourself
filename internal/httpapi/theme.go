package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"workstation-manager/internal/auth"
	"workstation-manager/internal/database"
	"workstation-manager/internal/theme"
)

// resolveTheme picks the palette for a request. A workstation override wins
// over the viewer's preference, which wins over the deployment default.
func (s *Server) resolveTheme(r *http.Request, workstationAccent string) theme.Palette {
	var userAccent string
	if user := currentUserPointer(r); user != nil {
		userAccent = user.AccentColor
	}
	return theme.Resolve(userAccent, workstationAccent)
}

// themeStylesheet serves the resolved palette as its own stylesheet. Emitting
// CSS from an endpoint rather than an inline <style> block keeps the strict
// style-src Content-Security-Policy intact.
//
// It resolves whatever context the request carries — a session, a share
// cookie, or neither — because the login and error pages are themed too, and a
// share recipient should see the workstation's colour.
func (s *Server) themeStylesheet(w http.ResponseWriter, r *http.Request) {
	palette := theme.Resolve(s.viewerAccent(r), s.workstationAccent(r))
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	// The palette follows the signed-in viewer, so caches must not share it.
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write([]byte(palette.CSS()))
}

// apiTheme publishes the palette as JSON so an app UI running inside a
// workstation can theme itself to match the controller. App traffic is proxied
// through the controller's own origin, so a page inside a workstation can call
// this with a plain relative fetch.
func (s *Server) apiTheme(w http.ResponseWriter, r *http.Request) {
	palette := theme.Resolve(s.viewerAccent(r), s.workstationAccent(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"theme": palette, "presets": theme.Presets, "default": theme.DefaultAccent,
	})
}

// viewerAccent returns the accent stored for whoever is making the request, or
// an empty string when the request carries no usable session.
func (s *Server) viewerAccent(r *http.Request) string {
	if user := currentUserPointer(r); user != nil {
		return user.AccentColor
	}
	cookie, err := r.Cookie("wm_session")
	if err != nil {
		return ""
	}
	user, err := s.db.SessionUser(r.Context(), auth.TokenHash(cookie.Value))
	if err != nil {
		return ""
	}
	return user.AccentColor
}

// workstationAccent finds the workstation a request belongs to, whether it
// arrived by explicit path, wildcard hostname, or share, and returns its
// override. Lookups are authorized the same way the matching page is.
func (s *Server) workstationAccent(r *http.Request) string {
	id := strings.TrimSpace(r.URL.Query().Get("workstation"))
	if id == "" {
		id = s.workstationHostname(r.Host)
	}
	if id == "" {
		return ""
	}
	if share := currentShare(r); share.WorkstationID != "" {
		if share.WorkstationID != id {
			return ""
		}
		ws, err := s.db.Workstation(r.Context(), id, database.User{IsAdmin: true})
		if err != nil {
			return ""
		}
		return ws.AccentColor
	}
	user := currentUserPointer(r)
	if user == nil {
		cookie, err := r.Cookie("wm_session")
		if err != nil {
			return ""
		}
		resolved, err := s.db.SessionUser(r.Context(), auth.TokenHash(cookie.Value))
		if err != nil {
			return ""
		}
		user = &resolved
	}
	ws, err := s.db.Workstation(r.Context(), id, *user)
	if err != nil {
		return ""
	}
	return ws.AccentColor
}

func (s *Server) appearancePage(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	s.render(w, "appearance.html", pageData{
		Title: "Appearance", User: &user,
		Theme: s.resolveTheme(r, ""), Presets: theme.Presets,
	})
}

func (s *Server) setUserAccent(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, errors.New("invalid appearance form"))
		return
	}
	accent, err := normalizeAccentInput(r.FormValue("accent_color"))
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	if err := s.db.SetUserAccent(r.Context(), currentUser(r).ID, accent); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	http.Redirect(w, r, "/appearance", http.StatusSeeOther)
}

func (s *Server) setWorkstationAccent(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, errors.New("invalid appearance form"))
		return
	}
	accent, err := normalizeAccentInput(r.FormValue("accent_color"))
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	id := r.PathValue("id")
	if err := s.db.SetWorkstationAccent(r.Context(), id, currentUser(r), accent); err != nil {
		s.renderError(w, r, http.StatusNotFound, errors.New("workstation not found"))
		return
	}
	http.Redirect(w, r, "/workstations/"+id, http.StatusSeeOther)
}

func (s *Server) apiSetUserAccent(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AccentColor string `json:"accent_color"`
	}
	if err := decodeAPIJSON(w, r, &input); err != nil {
		return
	}
	accent, err := normalizeAccentInput(input.AccentColor)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if err := s.db.SetUserAccent(r.Context(), currentUser(r).ID, accent); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, theme.Resolve(accent, ""))
}

func (s *Server) apiSetWorkstationAccent(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AccentColor string `json:"accent_color"`
	}
	if err := decodeAPIJSON(w, r, &input); err != nil {
		return
	}
	accent, err := normalizeAccentInput(input.AccentColor)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	user := currentUser(r)
	if err := s.db.SetWorkstationAccent(r.Context(), r.PathValue("id"), user, accent); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workstation not found"})
		return
	}
	writeJSON(w, http.StatusOK, theme.Resolve(user.AccentColor, accent))
}

// normalizeAccentInput accepts a colour or an explicit reset. Anything else is
// rejected rather than silently coerced, so a malformed value never reaches
// the stylesheet.
func normalizeAccentInput(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "default" {
		return "", nil
	}
	return theme.Normalize(value)
}
