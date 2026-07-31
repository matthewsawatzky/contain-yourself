package manifests

import (
	"os"
	"path/filepath"
	"testing"
)

func validManifest() Manifest {
	return Manifest{
		SchemaVersion: 1, ID: "terminal", Name: "Terminal", Version: "1.0.0",
		Runtime:   Runtime{Type: "container-service", Image: "example/terminal:1.0.0", InternalPort: 7681},
		Routing:   Routing{BasePath: "/apps/terminal/"},
		Network:   Network{Mode: "workstation-vpn"},
		Storage:   []Storage{{Type: "workspace", Target: "/workspace"}},
		Resources: Resources{DefaultMemoryMB: 512, DefaultCPU: 0.5},
		Health:    Health{Type: "http", Path: "/"},
	}
}

func TestValidateAllowsReviewedManifest(t *testing.T) {
	if err := Validate(validManifest(), ""); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsLatestAndDangerousCapability(t *testing.T) {
	manifest := validManifest()
	manifest.Runtime.Image = "example/terminal:latest"
	if Validate(manifest, "") == nil {
		t.Fatal("latest tag was accepted")
	}
	manifest = validManifest()
	manifest.Permissions.Capabilities = []string{"SYS_ADMIN"}
	if Validate(manifest, "") == nil {
		t.Fatal("dangerous capability was accepted")
	}
}

func TestValidateBoundsEnvironmentStorageOwnershipAndSharedMemory(t *testing.T) {
	manifest := validManifest()
	manifest.Runtime.Environment = map[string]string{"HARDEN_DESKTOP": "true"}
	manifest.Storage[0].OwnerUID = 1000
	manifest.Storage[0].OwnerGID = 1000
	manifest.Resources.ShmSizeMB = 256
	if err := Validate(manifest, ""); err != nil {
		t.Fatalf("reviewed runtime fields rejected: %v", err)
	}
	manifest.Runtime.Environment["DOCKER_HOST"] = "unix:///var/run/docker.sock"
	if Validate(manifest, "") == nil {
		t.Fatal("unapproved environment key was accepted")
	}
	manifest = validManifest()
	manifest.Storage[0].OwnerUID = 1000
	if Validate(manifest, "") == nil {
		t.Fatal("partial storage ownership was accepted")
	}
	manifest = validManifest()
	manifest.Resources.ShmSizeMB = 4096
	if Validate(manifest, "") == nil {
		t.Fatal("unbounded shared memory was accepted")
	}
}

func TestScannerRejectsUnknownFields(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bad")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte("schema_version: 1\nid: bad\nname: Bad\nversion: 1\nprivileged: true\n")
	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	entries := registry.Entries()
	if len(entries) != 1 || entries[0].Error == "" {
		t.Fatal("unknown unsafe field was not reported")
	}
}

func TestScanDirectoriesIncludesDigestAndRejectsOverrides(t *testing.T) {
	core := t.TempDir()
	installed := t.TempDir()
	for _, root := range []string{core, installed} {
		dir := filepath.Join(root, "terminal")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		manifest := `schema_version: 1
id: terminal
name: Terminal
version: 1.0.0
runtime:
  type: container-service
  image: example/terminal:1.0.0
  internal_port: 7681
routing:
  base_path: /apps/terminal/
network:
  mode: workstation-vpn
resources:
  default_memory_mb: 128
  default_cpu: 0.25
desktop:
  visible: true
`
		if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := ScanDirectories(core)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := registry.Entry("terminal")
	if !ok || len(entry.SHA256) != 64 {
		t.Fatalf("manifest digest missing: %#v", entry)
	}
	if _, err := ScanDirectories(core, installed); err == nil {
		t.Fatal("installed app overrode a core id")
	}
}
