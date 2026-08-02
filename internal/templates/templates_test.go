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

func TestValidateAcceptsMatchingEgressAndVPNFlag(t *testing.T) {
	exists := func(string) bool { return true }
	for _, pair := range []struct {
		egress      string
		vpnRequired bool
	}{
		{"direct", false}, {"host-gateway", false}, {"ipv6", false}, {"wireguard", true},
	} {
		template := Template{
			SchemaVersion: 1, ID: "example", Name: "Example",
			WorkspaceImage: "alpine:3.21", Apps: []string{"terminal"},
			CPU: 2, MemoryMB: 1024, PIDLimit: 256,
			Egress: pair.egress, VPNRequired: pair.vpnRequired,
		}
		if err := Validate(template, exists); err != nil {
			t.Errorf("egress %q with vpn_required %v rejected: %v",
				pair.egress, pair.vpnRequired, err)
		}
	}
}

// The two fields describe the same thing. A template whose pair contradicts
// itself should fail loudly rather than have one field silently win.
func TestValidateRejectsContradictoryEgressAndVPNFlag(t *testing.T) {
	exists := func(string) bool { return true }
	base := func() Template {
		return Template{
			SchemaVersion: 1, ID: "example", Name: "Example",
			WorkspaceImage: "alpine:3.21", Apps: []string{"terminal"},
			CPU: 2, MemoryMB: 1024, PIDLimit: 256,
		}
	}
	contradiction := base()
	contradiction.Egress, contradiction.VPNRequired = "wireguard", false
	if Validate(contradiction, exists) == nil {
		t.Error("wireguard egress was accepted without vpn_required")
	}
	reverse := base()
	reverse.Egress, reverse.VPNRequired = "direct", true
	if Validate(reverse, exists) == nil {
		t.Error("direct egress was accepted with vpn_required")
	}
	unknown := base()
	unknown.Egress = "carrier-pigeon"
	if Validate(unknown, exists) == nil {
		t.Error("an unknown egress mode was accepted")
	}
}

// Templates written before egress modes existed omit the field entirely.
func TestValidateAllowsTemplatesWithNoEgressField(t *testing.T) {
	exists := func(string) bool { return true }
	for _, vpnRequired := range []bool{true, false} {
		template := Template{
			SchemaVersion: 1, ID: "example", Name: "Example",
			WorkspaceImage: "alpine:3.21", Apps: []string{"terminal"},
			CPU: 2, MemoryMB: 1024, PIDLimit: 256, VPNRequired: vpnRequired,
		}
		if err := Validate(template, exists); err != nil {
			t.Errorf("template without egress (vpn_required %v) rejected: %v", vpnRequired, err)
		}
	}
}
