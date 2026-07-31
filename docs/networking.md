# Networking

## Separate paths

Management traffic and workstation egress have different purposes:

```text
browser → optional TLS proxy → controller → app port
app process → shared Gluetun namespace → tunnel → internet
```

The controller reaches an app over the fixed Compose management network. That
connection does not traverse the VPN. An app in a VPN template is created with
Docker network mode `container:wm-<id>-vpn`; it has no second interface that
could fall back to the host route.

Gluetun owns the tunnel, firewall kill switch and DNS protection. The worker
sets `FIREWALL_INPUT_PORTS` only to the reviewed internal app ports so the
controller can reach them. It waits for Docker's gateway health state before
starting app containers and probes each declared app health URL.

## Failure behavior

- No selected profile, an inaccessible profile, or an invalid stored
  configuration: provisioning is rejected before the VPN container starts.
- Tunnel never healthy: apps are not created; controller records an error.
- Tunnel later fails: Gluetun's firewall remains the egress enforcement point.
  Reconciliation and health polling should be paired with external alerting.
- Controller unavailable: application management access stops, but no direct
  public app port appears.

The controller sends the selected configuration over the authenticated
controller-to-worker connection. The worker creates the Gluetun container,
copies the configuration to `/gluetun/wireguard/wg0.conf` before it starts,
and never places the WireGuard private key in Docker environment variables.

## Reverse proxy and wildcard DNS

No Caddy, Nginx or other proxy is bundled. A proxy should preserve `Host`,
forward WebSocket upgrade headers, set a suitable long read timeout, terminate
TLS, and route both the controller hostname and the wildcard hostname to
`controller:8080`.

Example DNS:

```text
A/AAAA  workstations.example.com     → host
CNAME   *.workstations.example.com   → workstations.example.com
```

Set `PUBLIC_BASE_DOMAIN=workstations.example.com` and
`SECURE_COOKIES=true`. Do not rewrite `/apps/...` paths.

The default loopback binding is suitable for a proxy on the same host. Direct
LAN access requires `CONTROLLER_BIND=0.0.0.0` and appropriate firewall rules.
