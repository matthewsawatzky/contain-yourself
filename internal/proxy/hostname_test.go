package proxy

import "testing"

func TestWorkstationHostname(t *testing.T) {
	tests := map[string]string{
		"ws-1002.workstations.example.com":        "ws-1002",
		"WS-1002.WORKSTATIONS.EXAMPLE.COM:443":    "ws-1002",
		"workstations.example.com":                "",
		"nested.ws-1002.workstations.example.com": "",
		"attacker.example":                        "",
	}
	for input, expected := range tests {
		if actual := WorkstationHostname(input, "workstations.example.com"); actual != expected {
			t.Errorf("%q: got %q, want %q", input, actual, expected)
		}
	}
}
