// Package manifests scans and validates administrator-installed app packages.
package manifests

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Manifest struct {
	SchemaVersion int         `yaml:"schema_version" json:"schema_version"`
	ID            string      `yaml:"id" json:"id"`
	Name          string      `yaml:"name" json:"name"`
	Version       string      `yaml:"version" json:"version"`
	Description   string      `yaml:"description" json:"description"`
	Runtime       Runtime     `yaml:"runtime" json:"runtime"`
	Routing       Routing     `yaml:"routing" json:"routing"`
	Network       Network     `yaml:"network" json:"network"`
	Storage       []Storage   `yaml:"storage" json:"storage"`
	Resources     Resources   `yaml:"resources" json:"resources"`
	Health        Health      `yaml:"health" json:"health"`
	Permissions   Permissions `yaml:"permissions" json:"permissions"`
	Desktop       Desktop     `yaml:"desktop" json:"desktop"`
}

type Runtime struct {
	Type         string            `yaml:"type" json:"type"`
	Image        string            `yaml:"image" json:"image"`
	Command      []string          `yaml:"command" json:"command"`
	Environment  map[string]string `yaml:"environment" json:"environment,omitempty"`
	InternalPort int               `yaml:"internal_port" json:"internal_port"`
}

type Routing struct {
	BasePath    string `yaml:"base_path" json:"base_path"`
	StripPrefix bool   `yaml:"strip_prefix" json:"strip_prefix"`
	WebSocket   bool   `yaml:"websocket" json:"websocket"`
}

type Network struct {
	Mode string `yaml:"mode" json:"mode"`
}

type Storage struct {
	Type     string `yaml:"type" json:"type"`
	Target   string `yaml:"target" json:"target"`
	OwnerUID int    `yaml:"owner_uid" json:"owner_uid,omitempty"`
	OwnerGID int    `yaml:"owner_gid" json:"owner_gid,omitempty"`
}

type Resources struct {
	DefaultMemoryMB int     `yaml:"default_memory_mb" json:"default_memory_mb"`
	DefaultCPU      float64 `yaml:"default_cpu" json:"default_cpu"`
	ShmSizeMB       int     `yaml:"shm_size_mb" json:"shm_size_mb,omitempty"`
}

type Health struct {
	Type           string `yaml:"type" json:"type"`
	Path           string `yaml:"path" json:"path"`
	TimeoutSeconds int    `yaml:"timeout_seconds" json:"timeout_seconds"`
}

type Permissions struct {
	Capabilities []string `yaml:"capabilities" json:"capabilities"`
}

type Desktop struct {
	Visible       bool   `yaml:"visible" json:"visible"`
	Icon          string `yaml:"icon" json:"icon"`
	Role          string `yaml:"role" json:"role,omitempty"`
	DefaultWidth  int    `yaml:"default_width" json:"default_width"`
	DefaultHeight int    `yaml:"default_height" json:"default_height"`
	Singleton     bool   `yaml:"singleton" json:"singleton"`
}

// Desktop roles let a deployment replace the bundled launcher or add a full
// graphical desktop without the controller special-casing any app id.
const (
	// RoleLauncher renders the controller's own app launcher. It must be a
	// controller-ui app, and it is never listed as a tile on itself.
	RoleLauncher = "launcher"
	// RoleDesktop is a container-service app that provides a full graphical
	// desktop, such as a VNC or RDP session, in place of the launcher.
	RoleDesktop = "desktop"
)

type Entry struct {
	Manifest Manifest `json:"manifest"`
	Path     string   `json:"path"`
	SHA256   string   `json:"sha256,omitempty"`
	Error    string   `json:"error,omitempty"`
}

type Registry struct {
	entries map[string]Entry
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

func Scan(directory string) (*Registry, error) {
	items, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read apps directory: %w", err)
	}
	registry := &Registry{entries: make(map[string]Entry)}
	for _, item := range items {
		if !item.IsDir() {
			continue
		}
		path := filepath.Join(directory, item.Name(), "app.yaml")
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		entry := Entry{Path: path}
		if err != nil {
			entry.Error = err.Error()
			registry.entries[item.Name()] = entry
			continue
		}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&entry.Manifest); err != nil {
			entry.Error = err.Error()
			registry.entries[item.Name()] = entry
			continue
		}
		if err := Validate(entry.Manifest, filepath.Dir(path)); err != nil {
			entry.Error = err.Error()
		}
		sum := sha256.Sum256(data)
		entry.SHA256 = hex.EncodeToString(sum[:])
		key := entry.Manifest.ID
		if key == "" {
			key = item.Name()
		}
		if _, exists := registry.entries[key]; exists {
			entry.Error = "duplicate app id"
		}
		registry.entries[key] = entry
	}
	return registry, nil
}

// Load validates one app.yaml and returns its manifest plus source digest.
func Load(path string) (Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, err
	}
	entry := Entry{Path: path}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&entry.Manifest); err != nil {
		return Entry{}, err
	}
	if err := Validate(entry.Manifest, filepath.Dir(path)); err != nil {
		return Entry{}, err
	}
	sum := sha256.Sum256(data)
	entry.SHA256 = hex.EncodeToString(sum[:])
	return entry, nil
}

// ScanDirectories combines a read-only core registry and a writable installed
// registry. Duplicate IDs are rejected so installed packages cannot override
// core applications through directory ordering.
func ScanDirectories(directories ...string) (*Registry, error) {
	combined := &Registry{entries: make(map[string]Entry)}
	for _, directory := range directories {
		registry, err := Scan(directory)
		if err != nil {
			return nil, err
		}
		for key, entry := range registry.entries {
			if _, exists := combined.entries[key]; exists {
				return nil, fmt.Errorf("duplicate app id %q across app directories", key)
			}
			combined.entries[key] = entry
		}
	}
	return combined, nil
}

func Validate(m Manifest, packageDirectory string) error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("schema_version must be 1")
	}
	if !identifier.MatchString(m.ID) {
		return fmt.Errorf("id must match %s", identifier)
	}
	if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Version) == "" {
		return errors.New("name and version are required")
	}
	switch m.Runtime.Type {
	case "controller-ui":
		if m.Runtime.Image != "" || len(m.Runtime.Command) != 0 || m.Runtime.InternalPort != 0 {
			return errors.New("controller-ui apps cannot declare container runtime fields")
		}
	case "container-service":
		if !pinnedImage(m.Runtime.Image) {
			return errors.New("container-service image must use an explicit non-latest tag")
		}
		if m.Runtime.InternalPort < 1024 || m.Runtime.InternalPort > 65535 {
			return errors.New("internal_port must be between 1024 and 65535")
		}
	case "workspace-image":
		if !pinnedImage(m.Runtime.Image) {
			return errors.New("workspace-image requires a pinned image")
		}
	default:
		return errors.New("runtime.type must be controller-ui, container-service, or workspace-image")
	}
	if m.Runtime.Type == "container-service" {
		if !strings.HasPrefix(m.Routing.BasePath, "/apps/"+m.ID) || strings.Contains(m.Routing.BasePath, "..") {
			return errors.New("routing.base_path must begin with /apps/<app-id>")
		}
		if m.Network.Mode != "workstation-vpn" && m.Network.Mode != "management-only" {
			return errors.New("network.mode is not allowed")
		}
		allowedEnvironment := map[string]bool{
			"PUID": true, "PGID": true, "TZ": true, "HARDEN_DESKTOP": true,
			"DISABLE_OPEN_TOOLS": true, "DISABLE_SUDO": true,
			"DISABLE_TERMINALS": true, "CHROME_CLI": true,
		}
		for key, value := range m.Runtime.Environment {
			if !allowedEnvironment[key] {
				return fmt.Errorf("runtime environment key %q is not allowed", key)
			}
			if len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
				return fmt.Errorf("runtime environment value for %q is invalid", key)
			}
		}
	}
	targets := make(map[string]bool)
	for _, storage := range m.Storage {
		switch storage.Type {
		case "workspace", "app-data", "shell-home", "temporary":
		default:
			return fmt.Errorf("storage type %q is not allowed", storage.Type)
		}
		if !filepath.IsAbs(storage.Target) || strings.Contains(filepath.Clean(storage.Target), "..") {
			return fmt.Errorf("storage target %q must be an absolute container path", storage.Target)
		}
		if targets[storage.Target] {
			return fmt.Errorf("duplicate storage target %q", storage.Target)
		}
		if storage.OwnerUID < 0 || storage.OwnerUID > 65535 ||
			storage.OwnerGID < 0 || storage.OwnerGID > 65535 {
			return fmt.Errorf("storage owner for %q is outside the allowed range", storage.Target)
		}
		if (storage.OwnerUID == 0) != (storage.OwnerGID == 0) {
			return fmt.Errorf("storage owner for %q must declare both owner_uid and owner_gid", storage.Target)
		}
		targets[storage.Target] = true
	}
	for _, capability := range m.Permissions.Capabilities {
		if capability != "NET_RAW" {
			return fmt.Errorf("capability %q is not allowed for app containers", capability)
		}
	}
	if m.Health.Type != "" && m.Health.Type != "http" && m.Health.Type != "tcp" {
		return errors.New("health.type must be http or tcp")
	}
	switch m.Desktop.Role {
	case "":
	case RoleLauncher:
		if m.Runtime.Type != "controller-ui" {
			return errors.New("desktop.role launcher requires runtime.type controller-ui")
		}
	case RoleDesktop:
		if m.Runtime.Type != "container-service" {
			return errors.New("desktop.role desktop requires runtime.type container-service")
		}
	default:
		return fmt.Errorf("desktop.role %q is not allowed", m.Desktop.Role)
	}
	if m.Desktop.Icon != "" {
		icon := filepath.Clean(m.Desktop.Icon)
		if filepath.IsAbs(icon) || strings.Contains(icon, "..") {
			return errors.New("desktop.icon must remain inside the app package")
		}
		if packageDirectory != "" {
			if _, err := os.Stat(filepath.Join(packageDirectory, icon)); err != nil {
				return fmt.Errorf("desktop icon: %w", err)
			}
		}
	}
	if m.Resources.DefaultCPU < 0 || m.Resources.DefaultMemoryMB < 0 ||
		m.Resources.ShmSizeMB < 0 || m.Resources.ShmSizeMB > 2048 {
		return errors.New("resource defaults cannot be negative")
	}
	return nil
}

func pinnedImage(value string) bool {
	if value == "" || strings.ContainsAny(value, " \t\r\n\x00") {
		return false
	}
	if marker := strings.LastIndex(value, "@sha256:"); marker >= 0 {
		hash := value[marker+len("@sha256:"):]
		return marker > 0 && regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(hash)
	}
	lastSlash := strings.LastIndex(value, "/")
	lastColon := strings.LastIndex(value, ":")
	if lastColon <= lastSlash || lastColon == len(value)-1 {
		return false
	}
	return value[lastColon+1:] != "latest"
}

func (r *Registry) Get(id string) (Manifest, bool) {
	entry, ok := r.entries[id]
	return entry.Manifest, ok && entry.Error == ""
}

func (r *Registry) Entry(id string) (Entry, bool) {
	entry, ok := r.entries[id]
	return entry, ok && entry.Error == ""
}

func (r *Registry) Entries() []Entry {
	result := make([]Entry, 0, len(r.entries))
	for _, entry := range r.entries {
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Manifest.ID < result[j].Manifest.ID
	})
	return result
}
