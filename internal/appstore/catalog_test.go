package appstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildUpdatesAndThenChecksDeterministicCatalogue(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "apps", "sample")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(app, "app.yaml"), `schema_version: 1
id: sample
name: Sample
version: 1.2.3
description: Sample application
runtime:
  type: container-service
  image: example/sample:1.2.3
  internal_port: 7080
routing:
  base_path: /apps/sample/
  strip_prefix: true
network:
  mode: workstation-vpn
storage:
  - type: workspace
    target: /workspace
resources:
  default_memory_mb: 128
  default_cpu: 0.25
health:
  type: http
  path: /
desktop:
  visible: true
  icon: icon.svg
  default_width: 800
  default_height: 600
`)
	writeTestFile(t, filepath.Join(app, "icon.svg"), "<svg></svg>\n")
	writeTestFile(t, filepath.Join(app, "README.md"), "# Sample\n")
	bundle := Bundle{
		SchemaVersion: 1, ID: "sample", Name: "Sample", Version: "1.2.3",
		Summary:     "A useful sample application",
		Description: "A sufficiently detailed description of the sample application.",
		Categories:  []string{"utilities"},
		Authors:     []Author{{Name: "Example", URL: "https://example.com/author"}},
		License:     License{Catalog: "MIT", Upstream: "MIT"},
		Links: Links{
			Homepage: "https://example.com/app", Source: "https://example.com/source",
			Support: "https://example.com/support",
		},
		Compatibility: Compatibility{
			MinimumControllerVersion: "0.1.0", Platforms: []string{"linux/amd64"},
		},
		Icon: "icon.svg", Screenshots: []string{}, Package: Package{Files: []FileRecord{}},
	}
	writeTestJSON(t, filepath.Join(app, "bundle.json"), bundle)

	index, err := Build(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Apps) != 1 || index.Apps[0].Image != "example/sample:1.2.3" {
		t.Fatalf("unexpected index: %#v", index)
	}
	if index.Apps[0].Permissions.Network != "workstation-vpn" ||
		len(index.Apps[0].Permissions.Storage) != 1 {
		t.Fatalf("permissions were not derived from app.yaml: %#v", index.Apps[0].Permissions)
	}
	if err := WriteIndex(root, index); err != nil {
		t.Fatal(err)
	}
	if err := CheckIndex(root); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(app, "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"payload_sha256":`) ||
		!strings.Contains(string(data), `"README.md"`) {
		t.Fatalf("generated payload data missing: %s", data)
	}
}

func TestBuildRejectsUnknownBundleFieldsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "apps", "sample")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(app, "bundle.json"), `{"schema_version":1,"unknown":true}`)
	if _, err := Build(root, false); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown bundle field was not rejected: %v", err)
	}
}

func TestBuildDetectsStalePayload(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "apps", "sample")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(app, "app.yaml"), `schema_version: 1
id: sample
name: Sample
version: 1.0.0
description: Sample application
runtime:
  type: container-service
  image: example/sample:1.0.0
  internal_port: 7080
routing:
  base_path: /apps/sample/
network:
  mode: management-only
resources:
  default_memory_mb: 128
  default_cpu: 0.25
desktop:
  visible: true
  icon: icon.svg
`)
	writeTestFile(t, filepath.Join(app, "icon.svg"), "<svg></svg>\n")
	bundle := Bundle{
		SchemaVersion: 1, ID: "sample", Name: "Sample", Version: "1.0.0",
		Summary:     "A useful sample application",
		Description: "A sufficiently detailed description of the sample application.",
		Categories:  []string{"utilities"}, Authors: []Author{{Name: "Example"}},
		License: License{Catalog: "MIT", Upstream: "MIT"},
		Links: Links{
			Homepage: "https://example.com/app", Source: "https://example.com/source",
			Support: "https://example.com/support",
		},
		Compatibility: Compatibility{
			MinimumControllerVersion: "0.1.0", Platforms: []string{"linux/amd64"},
		},
		Icon: "icon.svg", Screenshots: []string{},
		Package: Package{
			PayloadSHA256: strings.Repeat("0", 64), PayloadSizeBytes: 1,
			Files: []FileRecord{},
		},
	}
	writeTestJSON(t, filepath.Join(app, "bundle.json"), bundle)
	if _, err := Build(root, false); err == nil || !strings.Contains(err.Error(), "hashes are stale") {
		t.Fatalf("stale payload was not rejected: %v", err)
	}
}

func writeTestFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
