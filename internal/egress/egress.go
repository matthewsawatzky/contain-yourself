// Package egress names the ways a workstation's traffic can reach the network.
//
// The mode is chosen in a template, stored on the workstation, carried to the
// worker, and finally handed to the WSLAN gateway as WSLAN_MODE. Keeping the
// vocabulary in one package means the controller, the worker, and the gateway
// cannot drift apart on what a mode is called or what it permits.
package egress

import "fmt"

type Mode string

const (
	// Direct NATs out of the Docker bridge, so the workstation reaches the
	// internet wearing the host's address. The control plane is unreachable.
	Direct Mode = "direct"

	// WireGuard forces every packet through the selected WireGuard profile and
	// fails closed: if the tunnel is down, nothing leaves.
	WireGuard Mode = "wireguard"

	// HostGateway is Direct plus reachability of services listening on the
	// Docker host, for a local model server or a development service. The
	// controller and worker stay unreachable.
	HostGateway Mode = "host-gateway"

	// DualStack is Direct with IPv6 alongside IPv4. It requires the Docker
	// daemon itself to have IPv6 enabled; without that, workstation creation
	// fails rather than silently falling back to IPv4.
	DualStack Mode = "ipv6"
)

// Default is used when a template does not name a mode and does not require a
// VPN.
const Default = Direct

var all = map[Mode]struct{}{
	Direct: {}, WireGuard: {}, HostGateway: {}, DualStack: {},
}

// All returns every mode in a stable order, for UI menus and documentation.
func All() []Mode { return []Mode{Direct, WireGuard, HostGateway, DualStack} }

// Description is the one-line explanation shown next to the mode in the UI.
func (m Mode) Description() string {
	switch m {
	case Direct:
		return "Host's internet connection. Appears to come from this machine's IP."
	case WireGuard:
		return "All traffic through a WireGuard profile. Fails closed if the tunnel drops."
	case HostGateway:
		return "Host's connection, and can also reach services running on the host."
	case DualStack:
		return "Host's connection with IPv6 alongside IPv4. Requires IPv6 on the Docker daemon."
	}
	return ""
}

// Label is the short human-readable name.
func (m Mode) Label() string {
	switch m {
	case Direct:
		return "Direct"
	case WireGuard:
		return "VPN (WireGuard)"
	case HostGateway:
		return "Direct + host access"
	case DualStack:
		return "Direct + IPv6"
	}
	return string(m)
}

// RequiresVPNProfile reports whether the mode is meaningless without a
// WireGuard profile attached.
func (m Mode) RequiresVPNProfile() bool { return m == WireGuard }

// RequiresIPv6 reports whether the workstation network must be created with
// IPv6 enabled.
func (m Mode) RequiresIPv6() bool { return m == DualStack }

// Valid reports whether the mode is one this build understands.
func (m Mode) Valid() bool {
	_, ok := all[m]
	return ok
}

// Parse validates a stored or submitted mode.
func Parse(value string) (Mode, error) {
	mode := Mode(value)
	if !mode.Valid() {
		return "", fmt.Errorf("egress mode %q is not supported", value)
	}
	return mode, nil
}

// Resolve derives the effective mode from a stored value and the legacy
// vpn_required flag.
//
// vpn_required predates named egress modes. Templates and workstation rows
// written before the modes existed carry an empty mode, so the flag remains the
// authority for those: true means WireGuard, false means the default. An
// explicit mode always wins, and the two are kept consistent at validation
// time so a stored pair can never disagree.
func Resolve(stored string, vpnRequired bool) Mode {
	if mode, err := Parse(stored); err == nil {
		return mode
	}
	if vpnRequired {
		return WireGuard
	}
	return Default
}
