# Networking

Every workstation has a private workstation-local network, or **WSLAN**:

```text
                           ┌─ app-browser (its own IP and port space)
controller → WSLAN ingress├─ app-terminal (its own IP and port space)
                           └─ app-code (its own IP and port space)
                                      │
                                      ▼
                         direct egress or WireGuard
```

The worker creates one Docker bridge with `internal=true` per workstation.
Docker does not give that bridge a host/internet exit. The only container
attached to both WSLAN and the Compose management network is the trusted WSLAN
gateway.

Each application has a small WSLAN sandbox container. The sandbox owns the
app's network namespace, stable DNS aliases (`app-<id>` and `<id>`), and a
default route pointing at the gateway. The untrusted app joins that namespace
with `container:<sandbox>` but does not receive `NET_ADMIN`. Apps can therefore
reuse the same internal port and communicate with each other using declared
ports without publishing host ports.

## Ingress

The controller does not connect directly to app containers. After normal
session and ownership checks, it proxies HTTP and WebSocket traffic to port
9000 on the workstation's WSLAN gateway. The request includes the internal
worker token and reviewed app ID. WSLAN accepts only the app ID/port map that
the worker supplied at creation and strips its private headers before
forwarding to `app-<id>:<declared-port>`.

Port 9000 is exposed only inside the Compose management network. It is not
published on the host. The same container port can be reused by every
workstation.

## Direct and WireGuard egress

Both choices use the same topology:

- **Direct:** WSLAN forwards and masquerades the workstation subnet through
  its outer interface, with an explicit deny for destinations on the Compose
  management subnet.
- **WireGuard:** WSLAN brings up the selected `wg0` configuration, permits
  forwarding only between the workstation subnet and `wg0`, and masquerades
  through the tunnel. The default forwarding policy is drop.

The encrypted profile is decrypted only for provisioning, sent over the
authenticated controller-to-worker connection, validated again, and copied to
`/run/wslan/wg0.conf` with mode `0600` before WSLAN starts. It is not placed in
Docker environment variables and app containers do not share the gateway
filesystem.

WSLAN provides DNS to app sandboxes. Docker's embedded resolver continues to
resolve workstation app aliases. In WireGuard mode, external queries use the
profile's DNS servers (or tunnel-routed defaults when none are declared).

## Startup and failure behavior

The worker starts resources in this order:

```text
WSLAN gateway → app network sandboxes → app containers
```

It waits for gateway health and then checks every declared app health URL
through WSLAN ingress. WireGuard health requires a recent peer handshake, not
merely the presence of a `wg0` interface. Stop uses the reverse order; restart
performs a complete ordered stop/start so sandbox routes are restored.

- Missing or invalid VPN profile: provisioning is rejected.
- WireGuard setup fails: WSLAN exits and apps are not started.
- Tunnel later fails: the private Docker network still has no independent
  internet path and WSLAN's forwarding policy does not permit management
  interface fallback.
- Controller unavailable: app management access stops, but no host app port
  appears.

## Reverse proxy and wildcard DNS

No Caddy, Nginx or other public proxy is bundled. A proxy should preserve
`Host`, forward WebSocket upgrades, terminate TLS, and route both the
controller hostname and wildcard hostname to `controller:7080`.

```text
A/AAAA  workstations.example.com     → host
CNAME   *.workstations.example.com   → workstations.example.com
```

Set `PUBLIC_BASE_DOMAIN=workstations.example.com` and
`SECURE_COOKIES=true`. The default loopback binding is suitable for a proxy on
the same host. Direct LAN access requires `CONTROLLER_BIND=0.0.0.0` and
appropriate firewall rules.

## Testing

An app image needs no WSLAN-specific code: it just listens on its declared
`internal_port` and, if `strip_prefix` is false, serves under its declared
`base_path`. `./dev app test <id>` (see
[development.md](development.md#adding-an-app)) provisions one app through the
exact gateway-plus-sandbox topology described above, using the app's real
image, and checks its declared health path through WSLAN ingress -- the same
way the worker does at provision time. `./dev app interop` checks the
multi-app case directly: two unmodified apps sharing one workstation network,
including sharing an internal port, still route correctly by app ID and can
reach each other by DNS alias with neither one aware the other exists.
