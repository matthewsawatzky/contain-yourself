package appstore

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workstation-manager/internal/manifests"
)

func TestManagerSynchronizesInstallsUpdatesAndRollsBack(t *testing.T) {
	source := t.TempDir()
	writeStoreVersion(t, source, "1.0.0", "first")

	root := t.TempDir()
	var approvals []string
	manager, err := NewManager(ManagerConfig{
		RootDirectory:          filepath.Join(root, "store"),
		InstalledAppsDirectory: filepath.Join(root, "apps"),
		IndexURL:               "http://localhost/index.json",
		ControllerVersion:      "1.0.0",
		Platform:               "linux/amd64",
		HTTPClient:             &http.Client{Transport: fileTransport{root: source}},
		Approve: func(_ context.Context, manifest manifests.Manifest, manifestPath string) error {
			if _, err := os.Stat(manifestPath); err != nil {
				return err
			}
			approvals = append(approvals, manifest.Version)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, err := manager.Install(context.Background(), "sample")
	if err != nil {
		t.Fatal(err)
	}
	if first.CurrentVersion != "1.0.0" || first.PreviousVersion != "" {
		t.Fatalf("first install = %#v", first)
	}
	active := filepath.Join(root, "apps", "sample", "app.yaml")
	if data, err := os.ReadFile(active); err != nil || !containsBytes(data, []byte("version: 1.0.0")) {
		t.Fatalf("active v1 manifest missing: %v %s", err, data)
	}

	writeStoreVersion(t, source, "1.1.0", "second")
	if _, err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	updated, err := manager.Install(context.Background(), "sample")
	if err != nil {
		t.Fatal(err)
	}
	if updated.CurrentVersion != "1.1.0" || updated.PreviousVersion != "1.0.0" {
		t.Fatalf("updated install = %#v", updated)
	}
	rolledBack, err := manager.Rollback(context.Background(), "sample")
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.CurrentVersion != "1.0.0" || rolledBack.PreviousVersion != "1.1.0" {
		t.Fatalf("rollback = %#v", rolledBack)
	}
	if len(approvals) != 3 {
		t.Fatalf("approval calls = %v", approvals)
	}
}

func TestManagerRejectsPayloadChangedAfterCatalogueSync(t *testing.T) {
	source := t.TempDir()
	writeStoreVersion(t, source, "1.0.0", "original")
	approved := false
	manager, err := NewManager(ManagerConfig{
		RootDirectory:          filepath.Join(t.TempDir(), "store"),
		InstalledAppsDirectory: filepath.Join(t.TempDir(), "apps"),
		IndexURL:               "http://localhost/index.json",
		ControllerVersion:      "1.0.0",
		Platform:               "linux/amd64",
		HTTPClient:             &http.Client{Transport: fileTransport{root: source}},
		Approve: func(context.Context, manifests.Manifest, string) error {
			approved = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, "apps", "sample", "README.md"), "tampered\n")
	if _, err := manager.Install(context.Background(), "sample"); err == nil {
		t.Fatal("tampered payload was installed")
	}
	if approved {
		t.Fatal("worker approval ran before payload verification")
	}
}

func TestManagerRetainsLastKnownGoodIndexAfterFailedSync(t *testing.T) {
	source := t.TempDir()
	writeStoreVersion(t, source, "1.0.0", "original")
	manager, err := NewManager(ManagerConfig{
		RootDirectory:          filepath.Join(t.TempDir(), "store"),
		InstalledAppsDirectory: filepath.Join(t.TempDir(), "apps"),
		IndexURL:               "http://localhost/index.json", ControllerVersion: "1.0.0",
		Platform:   "linux/amd64",
		HTTPClient: &http.Client{Transport: fileTransport{root: source}},
		Approve:    func(context.Context, manifests.Manifest, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, "index.json"), `{"schema_version":999}`)
	if _, err := manager.Sync(context.Background()); err == nil {
		t.Fatal("invalid replacement index was accepted")
	}
	views, status, err := manager.Views()
	if err != nil {
		t.Fatalf("last known good index was lost: %v", err)
	}
	if len(views) != 1 || views[0].Entry.ID != "sample" || status.Error == "" {
		t.Fatalf("unexpected cached view/status: %#v %#v", views, status)
	}
}

func writeStoreVersion(t *testing.T, root, version, marker string) {
	t.Helper()
	app := filepath.Join(root, "apps", "sample")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(app, "app.yaml"), `schema_version: 1
id: sample
name: Sample
version: `+version+`
description: Sample application
runtime:
  type: container-service
  image: example/sample:`+version+`
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
	writeTestFile(t, filepath.Join(app, "README.md"), marker+"\n")
	bundle := Bundle{
		SchemaVersion: 1, ID: "sample", Name: "Sample", Version: version,
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
		Package: Package{Files: []FileRecord{}},
	}
	writeTestJSON(t, filepath.Join(app, "bundle.json"), bundle)
	index, err := Build(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteIndex(root, index); err != nil {
		t.Fatal(err)
	}
}

func containsBytes(value, fragment []byte) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		match := true
		for offset := range fragment {
			if value[index+offset] != fragment[offset] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

type fileTransport struct {
	root string
}

func (transport fileTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	file := filepath.Join(transport.root,
		filepath.FromSlash(strings.TrimPrefix(request.URL.Path, "/")))
	data, err := os.ReadFile(file)
	if err != nil {
		return &http.Response{
			StatusCode: http.StatusNotFound, Status: "404 Not Found",
			Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil)),
			Request: request,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header),
		Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)),
		Request: request,
	}, nil
}
