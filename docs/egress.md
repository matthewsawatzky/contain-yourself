# Egress modes

A workstation's egress mode decides how its traffic reaches the network. It is
chosen in a template, stored on the workstation, sent to the worker, and handed
to the WSLAN gateway as `WSLAN_MODE`.

| Mode | Reaches the internet as | Can reach the host | Control plane |
| --- | --- | --- | --- |
| `direct` | the Docker host's address | no | blocked |
| `wireguard` | the VPN exit's address | no | blocked |
| `host-gateway` | the Docker host's address | yes | blocked |
| `ipv6` | the host's address, v4 and v6 | no | blocked |

In every mode the workstation is blocked from reaching the controller, the
worker, and other workstations' gateways. Apps never attach to the management
network and never publish host ports; WSLAN remains the only forwarding path.

## Choosing a mode

```yaml
# core_templates/private.yaml
vpn_required: true
egress: wireguard
```

`egress` and `vpn_required` describe the same decision from two angles.
`vpn_required` predates named modes, so it is kept and validated against the
mode: a template whose pair disagrees is rejected rather than having one field
silently win. The template builder in the UI writes both from one menu, so they
cannot diverge there.

A template with no `egress` field still works. An empty mode means "ask
`vpn_required`", which is how rows and files written before this feature keep
behaving exactly as they did.

## direct

The default. The gateway NATs out of the Docker bridge, so the workstation
appears to come from the host's IP. The management subnet is dropped in the
`FORWARD` chain.

## wireguard

Every packet goes through the selected WireGuard profile. The routing table is
built so that the tunnel is the only default route, and gateway health checks
both the interface and a recent handshake, so a dead tunnel marks the
workstation unhealthy instead of leaking to the host's connection.

This is the mode behind "make it look like I'm in the USA": upload a profile
whose exit is in the country you want, pick it at launch, and every app in the
workstation is behind it.

## host-gateway

`direct`, plus the workstation can reach services listening on the Docker host
— a local model server, a database, a development server. Implemented as an
explicit `ACCEPT` for the host gateway address appended *before* the management
subnet `DROP`, since iptables takes the first match.

The controller and worker share that subnet and stay unreachable. This is a
real widening of the boundary: a workstation in this mode can talk to anything
bound on the host, including services bound to `0.0.0.0` that you may not have
meant to expose. Use it deliberately.

## ipv6

`direct` with IPv6 alongside IPv4. The workstation network is created with
IPv6 enabled and the gateway applies the `ip6tables` equivalents of the v4
rules, including the control-plane drop.

**This mode requires IPv6 on the Docker daemon itself:**

```json
{ "ipv6": true, "fixed-cidr-v6": "fd00:dead:beef::/48" }
```

Without that, the gateway exits with a clear error at startup and the
workstation fails to provision rather than silently running v4-only. This mode
has been implemented and unit-tested but not verified end to end against a
v6-enabled daemon; treat it as unproven until you have tried it on your host.

## Changing a workstation's mode

The mode is fixed at creation. To change it, create a workstation from a
template with the mode you want. An update or rebuild preserves the existing
mode along with the labelled volumes.
