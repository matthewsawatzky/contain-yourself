package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"workstation-manager/internal/auth"
	"workstation-manager/internal/database"
)

type loginAttempt struct {
	Failures int
	Blocked  time.Time
	LastSeen time.Time
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
