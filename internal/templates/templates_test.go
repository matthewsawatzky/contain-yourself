package templates

import "testing"

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
