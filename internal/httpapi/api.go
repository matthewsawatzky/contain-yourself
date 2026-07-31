package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"workstation-manager/internal/auth"
	"workstation-manager/internal/database"
)

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]any{"status": "ok", "database": "ok", "worker": "ok"}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.worker.Health(ctx); err != nil {
		status["status"] = "degraded"
		status["worker"] = err.Error()
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) apiWorkstations(w http.ResponseWriter, r *http.Request) {
	items, err := s.db.ListWorkstations(r.Context(), currentUser(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) apiWorkstation(w http.ResponseWriter, r *http.Request) {
	ws, err := s.db.Workstation(r.Context(), r.PathValue("id"), currentUser(r))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workstation not found"})
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (s *Server) apiCreateWorkstation(w http.ResponseWriter, r *http.Request) {
	var input createInput
	if err := decodeAPIJSON(w, r, &input); err != nil {
		return
	}
	ws, err := s.create(r.Context(), currentUser(r), input)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, ws)
}

func (s *Server) apiWorkstationAction(w http.ResponseWriter, r *http.Request) {
	if err := s.beginAction(r.Context(), currentUser(r), r.PathValue("id"), r.PathValue("action")); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) apiUsage(w http.ResponseWriter, r *http.Request) {
	ws, err := s.db.Workstation(r.Context(), r.PathValue("id"), currentUser(r))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workstation not found"})
		return
	}
	usage, err := s.worker.Usage(r.Context(), ws.ID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func (s *Server) apiLogs(w http.ResponseWriter, r *http.Request) {
	ws, err := s.db.Workstation(r.Context(), r.PathValue("id"), currentUser(r))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workstation not found"})
		return
	}
	appID := r.PathValue("app")
	installed := false
	for _, app := range ws.Apps {
		installed = installed || app.AppID == appID
	}
	if !installed {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "app not installed"})
		return
	}
	tail := 200
	if raw := r.URL.Query().Get("tail"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tail must be between 1 and 1000"})
			return
		}
		tail = parsed
	}
	logs, err := s.worker.Logs(r.Context(), ws.ID, appID, tail)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

type shareInput struct {
	Permissions    []string `json:"permissions"`
	ExpiresHours   int      `json:"expires_hours"`
	MaxUses        int      `json:"max_uses"`
	NamedRecipient string   `json:"named_recipient"`
}

func (s *Server) apiShares(w http.ResponseWriter, r *http.Request) {
	ws, err := s.db.Workstation(r.Context(), r.PathValue("id"), currentUser(r))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workstation not found"})
		return
	}
	shares, err := s.db.ListShares(r.Context(), ws.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, shares)
}

func (s *Server) apiCreateShare(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	ws, err := s.db.Workstation(r.Context(), r.PathValue("id"), user)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workstation not found"})
		return
	}
	var input shareInput
	if err := decodeAPIJSON(w, r, &input); err != nil {
		return
	}
	permissions, expiresAt, maxUses, recipient, err := parseShareInput(
		input.Permissions, intString(input.ExpiresHours), intString(input.MaxUses),
		input.NamedRecipient)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	raw, hash, err := auth.RandomToken(32)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	share, err := s.db.CreateShare(r.Context(), ws.ID, user.ID, hash, permissions,
		recipient, expiresAt, maxUses)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_ = s.db.RecordEvent(r.Context(), ws.ID, "share.created",
		"Workstation share created for "+shareRecipient(recipient))
	writeJSON(w, http.StatusCreated, map[string]any{
		"share": share, "share_path": "/share/" + raw,
	})
}

func (s *Server) apiRevokeShare(w http.ResponseWriter, r *http.Request) {
	ws, err := s.db.Workstation(r.Context(), r.PathValue("id"), currentUser(r))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workstation not found"})
		return
	}
	shareID, err := strconv.ParseInt(r.PathValue("share"), 10, 64)
	if err != nil || shareID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid share id"})
		return
	}
	if err := s.db.RevokeShare(r.Context(), ws.ID, shareID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "share not found or already revoked"})
		return
	}
	_ = s.db.RecordEvent(r.Context(), ws.ID, "share.revoked", "Workstation share revoked")
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (s *Server) apiApps(w http.ResponseWriter, r *http.Request) {
	s.registryMu.RLock()
	defer s.registryMu.RUnlock()
	writeJSON(w, http.StatusOK, s.apps.Entries())
}

func (s *Server) apiAppStore(w http.ResponseWriter, r *http.Request) {
	views, status, err := s.store.Views()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": err.Error(), "sync": status, "apps": []any{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sync": status, "apps": views})
}

func (s *Server) apiAppStoreSync(w http.ResponseWriter, r *http.Request) {
	index, err := s.store.Sync(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, index)
}

func (s *Server) apiAppStoreInstall(w http.ResponseWriter, r *http.Request) {
	record, err := s.store.Install(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if err := s.rescan(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) apiAppStoreRollback(w http.ResponseWriter, r *http.Request) {
	record, err := s.store.Rollback(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if err := s.rescan(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) apiTemplates(w http.ResponseWriter, r *http.Request) {
	s.registryMu.RLock()
	defer s.registryMu.RUnlock()
	writeJSON(w, http.StatusOK, s.presets.All())
}

func (s *Server) apiVPNProfiles(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var profiles []database.VPNProfile
	var err error
	if user.IsAdmin {
		profiles, err = s.db.ListVPNProfiles(r.Context(), false)
	} else {
		profiles, err = s.db.ListVPNProfilesForUser(r.Context(), user, false)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, profiles)
}

func (s *Server) apiCreateVPNProfile(w http.ResponseWriter, r *http.Request) {
	var input vpnProfileInput
	if err := decodeAPIJSON(w, r, &input); err != nil {
		return
	}
	profile, err := s.storeVPNProfile(r.Context(), currentUser(r), input)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, profile)
}

func (s *Server) apiSetVPNProfileEnabled(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid VPN profile id"})
		return
	}
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeAPIJSON(w, r, &input); err != nil {
		return
	}
	if err := s.db.SetVPNProfileEnabled(r.Context(), id, currentUser(r), input.Enabled); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "VPN profile not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "enabled": input.Enabled})
}

func (s *Server) apiUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.db.ListUsers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) apiCreateUser(w http.ResponseWriter, r *http.Request) {
	var input userInput
	if err := decodeAPIJSON(w, r, &input); err != nil {
		return
	}
	user, err := s.storeUser(r.Context(), input)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (s *Server) apiRescan(w http.ResponseWriter, r *http.Request) {
	if err := s.rescan(); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rescanned"})
}

func (s *Server) apiReconcile(w http.ResponseWriter, r *http.Request) {
	if err := s.Reconcile(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reconciled"})
}

func (s *Server) apiBackup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path string `json:"path"`
	}
	if err := decodeAPIJSON(w, r, &input); err != nil {
		return
	}
	if input.Path == "" {
		input.Path = "/data/backups/controller-" + time.Now().UTC().Format("20060102-150405") + ".db"
	}
	clean := filepath.Clean(input.Path)
	if clean != "/data/backups" && len(clean) > len("/data/backups/") &&
		clean[:len("/data/backups/")] != "/data/backups/" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "backup path must be under /data/backups"})
		return
	}
	if err := s.db.Backup(r.Context(), clean); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "created", "path": clean})
}

func decodeAPIJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "trailing JSON is not allowed"})
		return errors.New("trailing JSON")
	}
	return nil
}

func intString(value int) string {
	return strconv.Itoa(value)
}
