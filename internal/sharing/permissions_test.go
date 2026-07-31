package sharing

import "testing"

func TestPermissionsRoundTrip(t *testing.T) {
	values, err := Validate([]string{"terminal-control", "open-apps", "open-apps"})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || !Has(values, TerminalControl) {
		t.Fatal("permissions were not normalized")
	}
	encoded, err := Encode(values)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil || !Has(decoded, OpenApps) {
		t.Fatalf("decode failed: values=%v err=%v", decoded, err)
	}
}

func TestUnknownPermissionRejected(t *testing.T) {
	if _, err := Validate([]string{"manage-workstation"}); err == nil {
		t.Fatal("unsafe permission was accepted")
	}
}
