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

func TestGrantsRoundTripThroughStorage(t *testing.T) {
	grants := []Mode{Direct, WireGuard, DualStack}
	restored := ParseGrants(FormatGrants(grants))
	if len(restored) != 3 {
		t.Fatalf("round trip = %v", restored)
	}
	for i, mode := range grants {
		if restored[i] != mode {
			t.Fatalf("round trip changed order or content: %v", restored)
		}
	}
	if got := FormatGrants(nil); got != "" {
		t.Fatalf("empty grants should encode to an empty string, got %q", got)
	}
}

// A grant set written by a newer build, or naming a mode this build dropped,
// must degrade to "not granted" rather than failing the whole set open or shut.
func TestParseGrantsSkipsUnknownEntries(t *testing.T) {
	grants := ParseGrants("direct,quantum-tunnel,,  wireguard  ,direct")
	if len(grants) != 2 || grants[0] != Direct || grants[1] != WireGuard {
		t.Fatalf("grants = %v, want [direct wireguard] with duplicates and junk dropped", grants)
	}
}

// An empty grant set is a revocation, not an invitation to fall back to a
// default. This is the test that would catch a fail-open regression.
func TestEmptyGrantsDenyEverything(t *testing.T) {
	for _, mode := range All() {
		if Granted(nil, mode, false) {
			t.Errorf("empty grants allowed %q", mode)
		}
	}
}

func TestGrantedHonoursTheGrantSet(t *testing.T) {
	grants := []Mode{Direct}
	if !Granted(grants, Direct, false) {
		t.Error("a granted mode was denied")
	}
	if Granted(grants, WireGuard, false) {
		t.Error("an ungranted mode was allowed")
	}
}

// host-gateway reaches services on the Docker host, so it is never grantable
// to an ordinary user even if it somehow appears in their stored set.
func TestHostGatewayIsAdministratorOnly(t *testing.T) {
	if !HostGateway.AdminOnly() {
		t.Fatal("host-gateway should be administrator-only")
	}
	if Granted([]Mode{HostGateway}, HostGateway, false) {
		t.Fatal("a stored host-gateway grant let a non-administrator through")
	}
	if !Granted(nil, HostGateway, true) {
		t.Fatal("an administrator was denied host-gateway")
	}
	if _, err := ValidateGrants([]string{"host-gateway"}); err == nil {
		t.Fatal("host-gateway was accepted as a user grant")
	}
	for _, mode := range Grantable() {
		if mode.AdminOnly() {
			t.Errorf("Grantable() offered administrator-only mode %q", mode)
		}
	}
}

func TestAdministratorsMayUseAnyValidMode(t *testing.T) {
	for _, mode := range All() {
		if !Granted(nil, mode, true) {
			t.Errorf("administrator denied %q", mode)
		}
	}
	if Granted(nil, Mode("nonsense"), true) {
		t.Error("administrator was allowed an invalid mode")
	}
}

func TestValidateGrantsRejectsUnknownModes(t *testing.T) {
	if _, err := ValidateGrants([]string{"direct", "carrier-pigeon"}); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
	valid, err := ValidateGrants([]string{"direct", "direct", "wireguard"})
	if err != nil {
		t.Fatal(err)
	}
	if len(valid) != 2 {
		t.Fatalf("duplicates were not collapsed: %v", valid)
	}
}
