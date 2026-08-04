package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workstation-manager/pkg/workerapi"
)

func TestParseAppsAllowsSharedPorts(t *testing.T) {
	apps, err := parseApps("browser=3000,terminal=3000")
	if err != nil {
		t.Fatal(err)
	}
	if apps["browser"] != 3000 || apps["terminal"] != 3000 {
		t.Fatalf("unexpected mappings: %#v", apps)
	}
}

func TestParseAppsRejectsArbitraryTargets(t *testing.T) {
	for _, value := range []string{
		"browser=http://example.com",
		"../browser=3000",
		"browser=0",
		"browser=70000",
	} {
		if _, err := parseApps(value); err == nil {
			t.Fatalf("accepted unsafe mapping %q", value)
		}
	}
}

func TestRecentHandshake(t *testing.T) {
	if !hasRecentHandshake("peer-key\t1000\n", 1100) {
		t.Fatal("recent handshake was rejected")
	}
	if hasRecentHandshake("peer-key\t1000\n", 1300) {
		t.Fatal("stale handshake was accepted")
	}
	if hasRecentHandshake("peer-key\t0\n", 1000) {
		t.Fatal("missing handshake was accepted")
	}
}

// A real `wg show wg0 dump`. The first line is the interface, and its second
// field is the private key.
const sampleDump = "cPrivateKeyMustNeverLeakAAAAAAAAAAAAAAAAAAA=\tsPublicKeyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\t51820\toff\n" +
	"peerPublicKeyAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\t(none)\t203.0.113.10:51820\t0.0.0.0/0\t1000\t4096\t2048\t25\n"

func TestParseWireGuardDumpReadsPeerState(t *testing.T) {
	tunnel := parseWireGuardDump(sampleDump, 1030)
	if !tunnel.Up {
		t.Fatal("a 30 second old handshake should count as up")
	}
	if tunnel.Endpoint != "203.0.113.10:51820" {
		t.Fatalf("endpoint = %q", tunnel.Endpoint)
	}
	if tunnel.HandshakeAgeSeconds != 30 {
		t.Fatalf("handshake age = %d, want 30", tunnel.HandshakeAgeSeconds)
	}
	if tunnel.ReceivedBytes != 4096 || tunnel.SentBytes != 2048 {
		t.Fatalf("transfer = %d/%d", tunnel.ReceivedBytes, tunnel.SentBytes)
	}
}

// The interface line holds the private key. If parsing ever starts at line 0,
// this catches it before the key reaches an HTTP response.
func TestParseWireGuardDumpNeverSurfacesKeyMaterial(t *testing.T) {
	tunnel := parseWireGuardDump(sampleDump, 1030)
	rendered, err := json.Marshal(tunnel)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered), "cPrivateKeyMustNeverLeak") {
		t.Fatalf("the private key reached the response: %s", rendered)
	}
	if strings.Contains(string(rendered), "peerPublicKey") {
		t.Fatalf("peer key material reached the response: %s", rendered)
	}
}

func TestParseWireGuardDumpTreatsStaleHandshakeAsDown(t *testing.T) {
	// 1000 + 181s: past the point WireGuard considers the session live.
	tunnel := parseWireGuardDump(sampleDump, 1181)
	if tunnel.Up {
		t.Fatal("a stale handshake was reported as up")
	}
	if tunnel.HandshakeAgeSeconds != 181 {
		t.Fatalf("handshake age = %d", tunnel.HandshakeAgeSeconds)
	}
}

func TestParseWireGuardDumpHandlesNeverHandshaked(t *testing.T) {
	never := "priv\tpub\t51820\toff\n" +
		"peer\t(none)\t203.0.113.10:51820\t0.0.0.0/0\t0\t0\t0\t25\n"
	tunnel := parseWireGuardDump(never, 5000)
	if tunnel.Up {
		t.Fatal("a tunnel with no handshake was reported as up")
	}
	if tunnel.HandshakeAgeSeconds != -1 {
		t.Fatalf("age = %d, want -1 for never", tunnel.HandshakeAgeSeconds)
	}
	if tunnel.Endpoint != "203.0.113.10:51820" {
		t.Fatalf("endpoint should still be reported: %q", tunnel.Endpoint)
	}
}

func TestParseWireGuardDumpToleratesJunk(t *testing.T) {
	for _, input := range []string{"", "\n", "only-an-interface-line", "a\tb\tc"} {
		tunnel := parseWireGuardDump(input, 1000)
		if tunnel.Up {
			t.Errorf("input %q produced an up tunnel", input)
		}
	}
}

// The status endpoint names the VPN exit, so it must not answer without the
// worker token.
func TestStatusRequiresTheToken(t *testing.T) {
	g := &gateway{token: "abcdefghijklmnopqrstuvwxyz012345", mode: "direct"}

	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	recorder := httptest.NewRecorder()
	g.status(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/status", nil)
	request.Header.Set(tokenHeader, g.token)
	recorder = httptest.NewRecorder()
	g.status(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d", recorder.Code)
	}
	var result workerapi.EgressStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Mode != "direct" || !result.Healthy || result.FailsClosed {
		t.Fatalf("direct status = %+v", result)
	}
	if result.Tunnel != nil {
		t.Fatal("direct mode should report no tunnel")
	}
}
