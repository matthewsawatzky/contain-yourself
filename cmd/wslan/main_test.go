package main

import "testing"

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
