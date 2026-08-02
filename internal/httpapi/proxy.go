package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"workstation-manager/internal/database"
	"workstation-manager/internal/proxy"
	"workstation-manager/internal/workstations"
)

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
	host := "wm-" + ws.ID + "-wslan"
	target, _ := url.Parse(fmt.Sprintf("http://%s:%d", host, 9000))
	reverse := httputil.NewSingleHostReverseProxy(target)
	original := reverse.Director
	reverse.Director = func(request *http.Request) {
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
		request.Header.Set("X-Contain-WSLAN-Token", s.config.WorkerToken)
		request.Header.Set("X-Contain-WSLAN-App", appID)
	}
	reverse.ModifyResponse = func(response *http.Response) error {
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
	reverse.ErrorHandler = func(rw http.ResponseWriter, request *http.Request, err error) {
		s.log.Warn("app proxy failed", "workstation_id", ws.ID, "app_id", appID, "error", err)
		http.Error(rw, "application is unavailable", http.StatusBadGateway)
	}
	reverse.ServeHTTP(w, r)
}

func (s *Server) workstationHostname(hostport string) string {
	return proxy.WorkstationHostname(hostport, s.config.PublicBaseDomain)
}
