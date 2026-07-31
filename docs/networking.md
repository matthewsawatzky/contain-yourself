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
copies the mode-`0600` configuration to
`/tmp/workstation-manager-wireguard.conf` before it starts, points Gluetun's
`WIREGUARD_CONF_SECRETFILE` at it, and never places the WireGuard private key
in Docker environment variables. Workstation applications share the gateway's
network namespace, not its filesystem.

## App-to-app networking

There is no separate per-workstation backbone LAN in v1. This is deliberate:
the original fail-closed design uses the VPN gateway's shared network namespace
instead of a custom router.

- In a VPN workstation, all app containers join the Gluetun container's
  network namespace. They share one loopback interface and can reach each
  other at `127.0.0.1:<internal-port>`. Internal ports must therefore be
  unique within the workstation.
- In a direct-internet workstation, app containers attach to the private
  Docker management network. Docker can resolve their deterministic container
  names, but those names are currently an implementation detail rather than a
  stable app API.
- Apps do not share their container filesystems. They communicate through
  declared ports and share only the Docker volumes requested in their
  manifests.
- No app port is published on the host. Browser traffic always enters through
  the authenticated controller proxy.

The management network is the present control/proxy path, not a general
service bus. If applications later need stable discovery, the next step should
be one private bridge network per workstation plus controller-issued service
names and explicit ingress/egress policy. That should be added as a versioned
feature instead of letting apps depend on Docker container names accidentally.

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
