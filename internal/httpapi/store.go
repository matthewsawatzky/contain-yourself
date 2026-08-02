package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"time"

	"workstation-manager/internal/manifests"
)

func (s *Server) appStorePage(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	views, status, err := s.store.Views()
	data := pageData{Title: "App store", User: &user, StoreApps: views, StoreStatus: status}
	if status.Error != "" {
		data.Error = "Last synchronization failed: " + status.Error
	}
	if err != nil {
		data.Notice = "Synchronize the catalogue to browse optional apps."
	}
	s.render(w, "app-store.html", data)
}

func (s *Server) appStoreSync(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if _, err := s.store.Sync(ctx); err != nil {
		s.renderError(w, r, http.StatusBadGateway, err)
		return
	}
	http.Redirect(w, r, "/app-store", http.StatusSeeOther)
}

func (s *Server) appStoreInstall(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if _, err := s.store.Install(ctx, r.PathValue("id")); err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	if err := s.rescan(); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	http.Redirect(w, r, "/app-store", http.StatusSeeOther)
}

func (s *Server) appStoreRollback(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if _, err := s.store.Rollback(ctx, r.PathValue("id")); err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	if err := s.rescan(); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	http.Redirect(w, r, "/app-store", http.StatusSeeOther)
}

func (s *Server) approveStoreManifest(ctx context.Context, manifest manifests.Manifest, manifestPath string) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	_, err = s.worker.ApproveApp(ctx, manifestAppSpec(manifest, hex.EncodeToString(sum[:])))
	return err
}
