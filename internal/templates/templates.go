// Package templates validates workstation presets. Templates configure the
// same workstation lifecycle; they do not create separate code paths.
package templates

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Template struct {
	SchemaVersion  int      `yaml:"schema_version" json:"schema_version"`
	ID             string   `yaml:"id" json:"id"`
	Name           string   `yaml:"name" json:"name"`
	Description    string   `yaml:"description" json:"description"`
	WorkspaceImage string   `yaml:"workspace_image" json:"workspace_image"`
	Apps           []string `yaml:"apps" json:"apps"`
	VPNRequired    bool     `yaml:"vpn_required" json:"vpn_required"`
	Persistent     bool     `yaml:"persistent" json:"persistent"`
	CPU            float64  `yaml:"cpu" json:"cpu"`
	MemoryMB       int      `yaml:"memory_mb" json:"memory_mb"`
	PIDLimit       int      `yaml:"pid_limit" json:"pid_limit"`
	ExpiresHours   int      `yaml:"expires_hours" json:"expires_hours"`
}

type Registry struct {
	templates map[string]Template
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

func Scan(directory string, appExists func(string) bool) (*Registry, error) {
	items, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read templates directory: %w", err)
	}
	registry := &Registry{templates: make(map[string]Template)}
	for _, item := range items {
		if item.IsDir() || !strings.HasSuffix(item.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(directory, item.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var template Template
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&template); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if err := Validate(template, appExists); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if _, exists := registry.templates[template.ID]; exists {
			return nil, fmt.Errorf("duplicate template id %q", template.ID)
		}
		registry.templates[template.ID] = template
	}
	if len(registry.templates) == 0 {
		return nil, errors.New("no valid templates found")
	}
	return registry, nil
}

func Validate(t Template, appExists func(string) bool) error {
	if t.SchemaVersion != 1 || !identifier.MatchString(t.ID) {
		return errors.New("invalid schema_version or template id")
	}
	if t.Name == "" || t.WorkspaceImage == "" || strings.HasSuffix(t.WorkspaceImage, ":latest") {
		return errors.New("name and pinned workspace_image are required")
	}
	if t.CPU <= 0 || t.MemoryMB < 128 || t.PIDLimit < 32 {
		return errors.New("resource limits are invalid")
	}
	if t.ExpiresHours < 0 {
		return errors.New("expires_hours cannot be negative")
	}
	seen := make(map[string]bool)
	for _, app := range t.Apps {
		if seen[app] {
			return fmt.Errorf("duplicate app %q", app)
		}
		if !appExists(app) {
			return fmt.Errorf("unknown or invalid app %q", app)
		}
		seen[app] = true
	}
	return nil
}

func (r *Registry) Get(id string) (Template, bool) {
	template, ok := r.templates[id]
	return template, ok
}

func (r *Registry) All() []Template {
	result := make([]Template, 0, len(r.templates))
	for _, template := range r.templates {
		result = append(result, template)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
