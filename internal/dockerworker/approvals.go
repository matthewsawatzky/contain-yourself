package dockerworker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"workstation-manager/pkg/workerapi"
)

type approvalStore struct {
	path    string
	mu      sync.RWMutex
	records map[string]workerapi.AppApprovalStatus
}

func newApprovalStore(path string) (*approvalStore, error) {
	if path == "" {
		return nil, errors.New("app approvals path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	store := &approvalStore{path: path, records: make(map[string]workerapi.AppApprovalStatus)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, store.persist()
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &store.records); err != nil {
		return nil, fmt.Errorf("decode app approvals: %w", err)
	}
	return store, nil
}

func (s *approvalStore) approve(app workerapi.AppSpec) (workerapi.AppApprovalStatus, error) {
	specification, err := specificationDigest(app)
	if err != nil {
		return workerapi.AppApprovalStatus{}, err
	}
	status := workerapi.AppApprovalStatus{
		ID: app.ID, Version: app.Version, Image: app.Image,
		ManifestSHA256: app.ManifestSHA256,
		Specification:  specification, ApprovedAt: time.Now().UTC(),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.records[approvalKey(app)]; !exists && len(s.records) >= 10000 {
		return workerapi.AppApprovalStatus{}, errors.New("app approval limit reached")
	}
	s.records[approvalKey(app)] = status
	if err := s.persistLocked(); err != nil {
		delete(s.records, approvalKey(app))
		return workerapi.AppApprovalStatus{}, err
	}
	return status, nil
}

func (s *approvalStore) allowed(app workerapi.AppSpec) bool {
	specification, err := specificationDigest(app)
	if err != nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	status, ok := s.records[approvalKey(app)]
	return ok && status.ID == app.ID && status.Version == app.Version &&
		status.Image == app.Image && status.ManifestSHA256 == app.ManifestSHA256 &&
		status.Specification == specification
}

func (s *approvalStore) persist() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistLocked()
}

func (s *approvalStore) persistLocked() error {
	data, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".approvals-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

func approvalKey(app workerapi.AppSpec) string {
	return app.ID + "\x00" + app.Version + "\x00" + app.ManifestSHA256
}

func specificationDigest(app workerapi.AppSpec) (string, error) {
	data, err := json.Marshal(app)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
