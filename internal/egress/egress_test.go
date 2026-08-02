package egress

import "testing"

func TestParseAcceptsEveryDeclaredModeAndNothingElse(t *testing.T) {
	for _, mode := range All() {
		parsed, err := Parse(string(mode))
		if err != nil || parsed != mode {
			t.Errorf("Parse(%q) = %q, %v", mode, parsed, err)
		}
	}
	for _, invalid := range []string{"", "vpn", "DIRECT", "host", "none", "ipv4"} {
		if _, err := Parse(invalid); err == nil {
			t.Errorf("Parse(%q) was accepted", invalid)
		}
	}
}

// Rows written before named modes existed carry an empty mode, so the legacy
// flag has to keep deciding for them.
func TestResolveFallsBackToTheLegacyVPNFlag(t *testing.T) {
	if mode := Resolve("", true); mode != WireGuard {
		t.Fatalf("Resolve(\"\", true) = %q, want wireguard", mode)
	}
	if mode := Resolve("", false); mode != Default {
		t.Fatalf("Resolve(\"\", false) = %q, want %q", mode, Default)
	}
}

func TestResolvePrefersAnExplicitMode(t *testing.T) {
	if mode := Resolve("host-gateway", false); mode != HostGateway {
		t.Fatalf("explicit mode was ignored: %q", mode)
	}
	// A stored value that is somehow unparseable must not crash or leak
	// through; it degrades to what the flag says.
	if mode := Resolve("nonsense", true); mode != WireGuard {
		t.Fatalf("invalid stored mode = %q, want the flag's answer", mode)
	}
}

func TestOnlyWireGuardNeedsAProfileAndOnlyDualStackNeedsIPv6(t *testing.T) {
	for _, mode := range All() {
		if got := mode.RequiresVPNProfile(); got != (mode == WireGuard) {
			t.Errorf("%q.RequiresVPNProfile() = %v", mode, got)
		}
		if got := mode.RequiresIPv6(); got != (mode == DualStack) {
			t.Errorf("%q.RequiresIPv6() = %v", mode, got)
		}
	}
}

func TestEveryModeIsDescribedForTheUI(t *testing.T) {
	for _, mode := range All() {
		if mode.Label() == "" || mode.Description() == "" {
			t.Errorf("mode %q has no label or description", mode)
		}
	}
}
