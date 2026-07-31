package templates

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateTemplate(t *testing.T) {
	value := Template{
		SchemaVersion: 1, ID: "developer", Name: "Developer",
		WorkspaceImage: "example/workspace:1.0.0", Apps: []string{"terminal"},
		CPU: 2, MemoryMB: 4096, PIDLimit: 512,
	}
	if err := Validate(value, func(id string) bool { return id == "terminal" }); err != nil {
		t.Fatal(err)
	}
	value.Apps = append(value.Apps, "missing")
	if Validate(value, func(id string) bool { return id == "terminal" }) == nil {
		t.Fatal("unknown app was accepted")
	}
}

func TestSaveAndDeleteCustomTemplate(t *testing.T) {
	directory := t.TempDir()
	value := Template{
		SchemaVersion: 1, ID: "custom-browser", Name: "Browser",
		WorkspaceImage: "alpine:3.21", Apps: []string{"browser"},
		CPU: 2, MemoryMB: 4096, PIDLimit: 512, Persistent: true,
	}
	appExists := func(id string) bool { return id == "browser" }
	if err := SaveCustom(directory, value, appExists); err != nil {
		t.Fatal(err)
	}
	registry, err := Scan(directory, appExists)
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := registry.Get(value.ID)
	if !ok || !saved.Custom || saved.Name != value.Name {
		t.Fatalf("saved template = %#v, %v", saved, ok)
	}
	if err := DeleteCustom(directory, value.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, value.ID+".yaml")); !os.IsNotExist(err) {
		t.Fatalf("custom template still exists: %v", err)
	}
}

func TestCustomTemplateCannotOverwriteCoreFilename(t *testing.T) {
	value := Template{
		SchemaVersion: 1, ID: "private", Name: "Private",
		WorkspaceImage: "alpine:3.21", Apps: []string{"terminal"},
		CPU: 1, MemoryMB: 1024, PIDLimit: 128,
	}
	if err := SaveCustom(t.TempDir(), value, func(string) bool { return true }); err == nil {
		t.Fatal("core template id was accepted for a custom template")
	}
}
