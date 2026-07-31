package appstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"workstation-manager/internal/manifests"
)

type ManagerConfig struct {
	RootDirectory          string
	InstalledAppsDirectory string
	IndexURL               string
	ControllerVersion      string
	HTTPClient             *http.Client
	Approve                func(context.Context, manifests.Manifest, string) error
	ReservedIDs            []string
	Platform               string
}

type Manager struct {
	config ManagerConfig
	http   *http.Client
	mu     sync.Mutex
}

type SyncStatus struct {
	IndexURL string    `json:"index_url"`
	ETag     string    `json:"etag,omitempty"`
	SyncedAt time.Time `json:"synced_at,omitempty"`
	Error    string    `json:"error,omitempty"`
}

type InstalledRecord struct {
	ID              string    `json:"id"`
	CurrentVersion  string    `json:"current_version"`
	PreviousVersion string    `json:"previous_version,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type StoreState struct {
	Installed map[string]InstalledRecord `json:"installed"`
}

type AppView struct {
	Entry             IndexEntry
	Installed         bool
	CurrentVersion    string
	PreviousVersion   string
	UpdateAvailable   bool
	Compatible        bool
	CompatibilityNote string
}

func NewManager(config ManagerConfig) (*Manager, error) {
	if strings.TrimSpace(config.RootDirectory) == "" ||
		strings.TrimSpace(config.InstalledAppsDirectory) == "" {
		return nil, errors.New("app-store root and installed-app directories are required")
	}
	if err := validateSourceURL(config.IndexURL); err != nil {
		return nil, err
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	client := *config.HTTPClient
	previousRedirect := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) == 0 || request.URL.Scheme != via[0].URL.Scheme ||
			request.URL.Host != via[0].URL.Host {
			return errors.New("app-store redirect escaped the configured origin")
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		if len(via) >= 10 {
			return errors.New("too many app-store redirects")
		}
		return nil
	}
	if config.Approve == nil {
		return nil, errors.New("app-store approval callback is required")
	}
	for _, directory := range []string{
		config.RootDirectory,
		filepath.Join(config.RootDirectory, "catalog"),
		filepath.Join(config.RootDirectory, "versions"),
		config.InstalledAppsDirectory,
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create app-store directory: %w", err)
		}
	}
	manager := &Manager{config: config, http: &client}
	if _, err := os.Stat(manager.statePath()); errors.Is(err, os.ErrNotExist) {
		if err := manager.writeState(StoreState{Installed: map[string]InstalledRecord{}}); err != nil {
			return nil, err
		}
	}
	return manager, nil
}

func (m *Manager) Sync(ctx context.Context) (Index, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	status, _ := m.loadSyncStatus()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.config.IndexURL, nil)
	if err != nil {
		return Index{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "contain-yourself-controller")
	if status.IndexURL == m.config.IndexURL && status.ETag != "" {
		request.Header.Set("If-None-Match", status.ETag)
	}
	response, err := m.http.Do(request)
	if err != nil {
		m.recordSyncError(status, err)
		return Index{}, fmt.Errorf("fetch app-store index: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		index, loadErr := m.loadIndex()
		if loadErr != nil {
			return Index{}, errors.New("store returned not-modified but no valid cached index exists")
		}
		status.SyncedAt = time.Now().UTC()
		status.Error = ""
		_ = m.writeSyncStatus(status)
		return index, nil
	}
	if response.StatusCode != http.StatusOK {
		err := fmt.Errorf("app store returned %s", response.Status)
		m.recordSyncError(status, err)
		return Index{}, err
	}
	data, err := readBounded(response.Body, 1024*1024)
	if err != nil {
		m.recordSyncError(status, err)
		return Index{}, err
	}
	index, err := DecodeIndex(data)
	if err != nil {
		m.recordSyncError(status, err)
		return Index{}, err
	}
	if err := atomicWrite(filepath.Join(m.config.RootDirectory, "catalog", "index.json"), data, 0o600); err != nil {
		return Index{}, err
	}
	status = SyncStatus{
		IndexURL: m.config.IndexURL, ETag: response.Header.Get("ETag"),
		SyncedAt: time.Now().UTC(),
	}
	if err := m.writeSyncStatus(status); err != nil {
		return Index{}, err
	}
	return index, nil
}

func (m *Manager) Views() ([]AppView, SyncStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index, err := m.loadIndex()
	status, _ := m.loadSyncStatus()
	if err != nil {
		return nil, status, err
	}
	state, err := m.loadState()
	if err != nil {
		return nil, status, err
	}
	views := make([]AppView, 0, len(index.Apps))
	for _, entry := range index.Apps {
		view := AppView{Entry: entry, Compatible: true}
		if installed, ok := state.Installed[entry.ID]; ok {
			view.Installed = true
			view.CurrentVersion = installed.CurrentVersion
			view.PreviousVersion = installed.PreviousVersion
			view.UpdateAvailable = compareVersions(entry.Version, installed.CurrentVersion) > 0
		} else if installed, loadErr := manifests.Load(filepath.Join(
			m.config.InstalledAppsDirectory, entry.ID, "app.yaml")); loadErr == nil &&
			installed.Manifest.ID == entry.ID {
			view.Installed = true
			view.CurrentVersion = installed.Manifest.Version
			view.UpdateAvailable = compareVersions(entry.Version, installed.Manifest.Version) > 0
		}
		if m.config.ControllerVersion != "" &&
			compareVersions(m.config.ControllerVersion, entry.Compatibility.MinimumControllerVersion) < 0 {
			view.Compatible = false
			view.CompatibilityNote = "Requires controller " +
				entry.Compatibility.MinimumControllerVersion + " or newer"
		}
		if m.config.Platform != "" && !contains(entry.Compatibility.Platforms, m.config.Platform) {
			view.Compatible = false
			view.CompatibilityNote = "Not published for " + m.config.Platform
		}
		views = append(views, view)
	}
	return views, status, nil
}

func (m *Manager) Install(ctx context.Context, id string) (InstalledRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index, err := m.loadIndex()
	if err != nil {
		return InstalledRecord{}, err
	}
	entry, ok := findEntry(index, id)
	if !ok {
		return InstalledRecord{}, errors.New("app is not present in the cached catalogue")
	}
	for _, reserved := range m.config.ReservedIDs {
		if id == reserved {
			return InstalledRecord{}, errors.New("core applications cannot be replaced by the app store")
		}
	}
	if m.config.ControllerVersion != "" &&
		compareVersions(m.config.ControllerVersion, entry.Compatibility.MinimumControllerVersion) < 0 {
		return InstalledRecord{}, fmt.Errorf("app requires controller %s or newer",
			entry.Compatibility.MinimumControllerVersion)
	}
	if m.config.Platform != "" && !contains(entry.Compatibility.Platforms, m.config.Platform) {
		return InstalledRecord{}, fmt.Errorf("app is not published for %s", m.config.Platform)
	}
	bundleURL, err := resolveStorePath(m.config.IndexURL, entry.Bundle.Path)
	if err != nil {
		return InstalledRecord{}, err
	}
	bundleData, err := m.fetchVerified(ctx, bundleURL, entry.Bundle)
	if err != nil {
		return InstalledRecord{}, err
	}
	bundle, err := DecodeBundle(bundleData)
	if err != nil {
		return InstalledRecord{}, err
	}
	if bundle.ID != entry.ID || bundle.Name != entry.Name || bundle.Version != entry.Version ||
		bundle.Icon != filepath.Base(entry.Icon) {
		return InstalledRecord{}, errors.New("bundle metadata does not match the catalogue index")
	}
	if len(bundle.Package.Files) == 0 || len(bundle.Package.Files) > 128 ||
		!digest.MatchString(bundle.Package.PayloadSHA256) ||
		bundle.Package.PayloadSizeBytes < 1 || bundle.Package.PayloadSizeBytes > maxPayloadBytes {
		return InstalledRecord{}, errors.New("bundle payload metadata is invalid")
	}

	incoming, err := os.MkdirTemp(m.config.RootDirectory, ".incoming-")
	if err != nil {
		return InstalledRecord{}, err
	}
	defer os.RemoveAll(incoming)
	appsRoot := filepath.Join(incoming, "apps")
	packageDirectory := filepath.Join(appsRoot, id)
	if err := os.MkdirAll(packageDirectory, 0o700); err != nil {
		return InstalledRecord{}, err
	}
	bundleBase, _ := url.Parse(bundleURL)
	bundleBase.Path = path.Dir(bundleBase.Path) + "/"
	bundleBase.RawQuery = ""
	bundleBase.Fragment = ""
	for _, file := range bundle.Package.Files {
		if err := validateDownloadRecord(file); err != nil {
			return InstalledRecord{}, err
		}
		fileURL, err := resolveStorePath(bundleBase.String(), file.Path)
		if err != nil {
			return InstalledRecord{}, err
		}
		data, err := m.fetchVerified(ctx, fileURL, file)
		if err != nil {
			return InstalledRecord{}, fmt.Errorf("%s: %w", file.Path, err)
		}
		target := filepath.Join(packageDirectory, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return InstalledRecord{}, err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return InstalledRecord{}, err
		}
	}
	payload, err := inspectPayload(packageDirectory)
	if err != nil {
		return InstalledRecord{}, err
	}
	if !reflect.DeepEqual(payload, bundle.Package) {
		return InstalledRecord{}, errors.New("downloaded package payload does not match bundle integrity data")
	}
	registry, err := manifests.Scan(appsRoot)
	if err != nil {
		return InstalledRecord{}, err
	}
	manifest, ok := registry.Get(id)
	if !ok {
		return InstalledRecord{}, errors.New("downloaded app manifest is invalid")
	}
	if err := validateBundle(bundle, manifest, id, packageDirectory); err != nil {
		return InstalledRecord{}, err
	}
	manifestPath := filepath.Join(packageDirectory, "app.yaml")
	if err := m.config.Approve(ctx, manifest, manifestPath); err != nil {
		return InstalledRecord{}, fmt.Errorf("worker approval: %w", err)
	}
	versionDirectory := filepath.Join(m.config.RootDirectory, "versions", id, bundle.Version)
	if _, err := os.Stat(versionDirectory); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(versionDirectory), 0o700); err != nil {
			return InstalledRecord{}, err
		}
		if err := copyDirectory(packageDirectory, versionDirectory); err != nil {
			return InstalledRecord{}, err
		}
	} else if err != nil {
		return InstalledRecord{}, err
	} else {
		existing, err := inspectPayload(versionDirectory)
		if err != nil || !reflect.DeepEqual(existing, payload) {
			return InstalledRecord{}, errors.New("stored app version conflicts with catalogue payload")
		}
	}
	state, err := m.loadState()
	if err != nil {
		return InstalledRecord{}, err
	}
	current := state.Installed[id]
	record := InstalledRecord{
		ID: id, CurrentVersion: bundle.Version, UpdatedAt: time.Now().UTC(),
	}
	if current.CurrentVersion != "" && current.CurrentVersion != bundle.Version {
		record.PreviousVersion = current.CurrentVersion
	} else {
		record.PreviousVersion = current.PreviousVersion
	}
	if err := m.activate(id, versionDirectory); err != nil {
		return InstalledRecord{}, err
	}
	state.Installed[id] = record
	if err := m.writeState(state); err != nil {
		return InstalledRecord{}, err
	}
	return record, nil
}

func (m *Manager) Rollback(ctx context.Context, id string) (InstalledRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.loadState()
	if err != nil {
		return InstalledRecord{}, err
	}
	current, ok := state.Installed[id]
	if !ok || current.PreviousVersion == "" {
		return InstalledRecord{}, errors.New("no previous app version is available")
	}
	versionDirectory := filepath.Join(m.config.RootDirectory, "versions", id, current.PreviousVersion)
	entry, err := manifests.Load(filepath.Join(versionDirectory, "app.yaml"))
	if err != nil || entry.Manifest.ID != id {
		return InstalledRecord{}, errors.New("previous app manifest is invalid")
	}
	if err := m.config.Approve(ctx, entry.Manifest, filepath.Join(versionDirectory, "app.yaml")); err != nil {
		return InstalledRecord{}, fmt.Errorf("worker approval: %w", err)
	}
	record := InstalledRecord{
		ID: id, CurrentVersion: current.PreviousVersion,
		PreviousVersion: current.CurrentVersion, UpdatedAt: time.Now().UTC(),
	}
	if err := m.activate(id, versionDirectory); err != nil {
		return InstalledRecord{}, err
	}
	state.Installed[id] = record
	if err := m.writeState(state); err != nil {
		return InstalledRecord{}, err
	}
	return record, nil
}

func (m *Manager) activate(id, source string) error {
	incoming, err := os.MkdirTemp(m.config.InstalledAppsDirectory, ".activate-"+id+"-")
	if err != nil {
		return err
	}
	os.Remove(incoming)
	defer os.RemoveAll(incoming)
	if err := copyDirectory(source, incoming); err != nil {
		return err
	}
	active := filepath.Join(m.config.InstalledAppsDirectory, id)
	previous := filepath.Join(m.config.InstalledAppsDirectory, ".previous-"+id)
	_ = os.RemoveAll(previous)
	if err := os.Rename(active, previous); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(incoming, active); err != nil {
		_ = os.Rename(previous, active)
		return err
	}
	return os.RemoveAll(previous)
}

func (m *Manager) fetchVerified(ctx context.Context, location string, record FileRecord) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "contain-yourself-controller")
	response, err := m.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned %s", response.Status)
	}
	data, err := readBounded(response.Body, record.SizeBytes)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != record.SizeBytes {
		return nil, errors.New("download size does not match catalogue")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != record.SHA256 {
		return nil, errors.New("download SHA-256 does not match catalogue")
	}
	return data, nil
}

func (m *Manager) loadIndex() (Index, error) {
	data, err := os.ReadFile(filepath.Join(m.config.RootDirectory, "catalog", "index.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Index{}, errors.New("app store has not been synchronized")
		}
		return Index{}, err
	}
	return DecodeIndex(data)
}

func (m *Manager) statePath() string {
	return filepath.Join(m.config.RootDirectory, "state.json")
}

func (m *Manager) loadState() (StoreState, error) {
	var state StoreState
	if err := readJSONFile(m.statePath(), &state); err != nil {
		return StoreState{}, err
	}
	if state.Installed == nil {
		state.Installed = make(map[string]InstalledRecord)
	}
	return state, nil
}

func (m *Manager) writeState(state StoreState) error {
	data, err := marshalJSON(state)
	if err != nil {
		return err
	}
	return atomicWrite(m.statePath(), data, 0o600)
}

func (m *Manager) loadSyncStatus() (SyncStatus, error) {
	var status SyncStatus
	err := readJSONFile(filepath.Join(m.config.RootDirectory, "catalog", "status.json"), &status)
	if errors.Is(err, os.ErrNotExist) {
		return SyncStatus{IndexURL: m.config.IndexURL}, nil
	}
	return status, err
}

func (m *Manager) writeSyncStatus(status SyncStatus) error {
	data, err := marshalJSON(status)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(m.config.RootDirectory, "catalog", "status.json"), data, 0o600)
}

func (m *Manager) recordSyncError(status SyncStatus, syncErr error) {
	status.IndexURL = m.config.IndexURL
	status.Error = syncErr.Error()
	_ = m.writeSyncStatus(status)
}

func findEntry(index Index, id string) (IndexEntry, bool) {
	for _, entry := range index.Apps {
		if entry.ID == id {
			return entry, true
		}
	}
	return IndexEntry{}, false
}

func validateDownloadRecord(record FileRecord) error {
	clean := path.Clean(record.Path)
	if record.Path == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") ||
		strings.HasPrefix(record.Path, "/") || strings.Contains(record.Path, "\\") ||
		!digest.MatchString(record.SHA256) || record.SizeBytes < 0 || record.SizeBytes > maxFileBytes {
		return fmt.Errorf("unsafe payload record %q", record.Path)
	}
	if clean == "bundle.json" {
		return errors.New("bundle.json cannot be part of its own payload")
	}
	return nil
}

func resolveStorePath(base, relative string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	target, err := baseURL.Parse(relative)
	if err != nil {
		return "", err
	}
	if target.Scheme != baseURL.Scheme || target.Host != baseURL.Host ||
		target.User != nil || target.Fragment != "" {
		return "", errors.New("store path escaped the configured origin")
	}
	return target.String(), nil
}

func validateSourceURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("APP_STORE_INDEX_URL is invalid")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	host := parsed.Hostname()
	if parsed.Scheme == "http" && (host == "127.0.0.1" || host == "::1" || host == "localhost") {
		return nil
	}
	return errors.New("APP_STORE_INDEX_URL must use HTTPS")
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	if limit < 1 {
		return nil, errors.New("download size limit is invalid")
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download exceeds %d bytes", limit)
	}
	return data, nil
}

func readJSONFile(file string, output any) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("JSON file contains trailing data")
	}
	return nil
}

func atomicWrite(file string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(file), ".write-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(mode); err != nil {
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
	return os.Rename(name, file)
}

func copyDirectory(source, destination string) error {
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(file string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, file)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symbolic links are not allowed in app packages")
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !entry.Type().IsRegular() {
			return errors.New("special files are not allowed in app packages")
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}

func compareVersions(left, right string) int {
	var a, b [3]int
	fmt.Sscanf(strings.TrimPrefix(left, "v"), "%d.%d.%d", &a[0], &a[1], &a[2])
	fmt.Sscanf(strings.TrimPrefix(right, "v"), "%d.%d.%d", &b[0], &b[1], &b[2])
	for index := range a {
		if a[index] < b[index] {
			return -1
		}
		if a[index] > b[index] {
			return 1
		}
	}
	return 0
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
