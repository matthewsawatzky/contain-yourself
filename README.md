# Workstation Manager

Workstation Manager is a lightweight, self-hosted controller for isolated
container workstations. A workstation is presented as one remote computer but
is implemented as a labelled set of Docker containers and volumes. The
controller owns authentication, SQLite state, the dashboard, authorization and
app proxying. A separate, internal-only worker is the only service that can
access Docker.

```text
browser ──HTTP──> controller ──narrow API──> docker-worker ──> Docker
                      │
                      ├── /data/controller.db
                      └── authenticated proxy ──> workstation apps
```

No reverse proxy is bundled. The default URL is
<http://127.0.0.1:8080>.

## Quick installation

Requirements: Docker Engine 24+ with Compose v2, Linux host support for
`/dev/net/tun` when using VPN workstations, and enough disk for selected images.

Run the interactive bootstrap from any accessible directory:

```bash
curl -fsSL https://github.com/matthewsawatzky/contain-yourself/releases/latest/download/bootstrap.sh | sh
```

It asks whether to create a dedicated `contain-yourself/` directory, use the
current directory, or use another path. It downloads `compose.yaml`,
`setup.sh`, and `update.sh`, verifies and installs the core configuration
bundle, generates the private worker token, and creates `.env`, `config/`, and
`data/`.

The bootstrap does not pull images or start Docker. It prints the selected
directory and leaves the final command to you:

```bash
cd contain-yourself
docker compose up -d
```

Compose fetches the exact tagged controller, worker, and WSLAN images from
GitHub Container Registry. All persistent files remain in the directory you
created.

Open <http://127.0.0.1:8080> and create the initial administrator. The
Terminal and Private workstation templates can be used immediately. Install
optional Browser, Code, and Files applications from **App store**. Before
launching a Private workstation, open **VPN profiles** and paste a WireGuard
configuration from your VPN provider. No VPN account credentials belong in
`.env`.

Common management commands are standard Compose commands:

```bash
docker compose ps
docker compose logs -f
docker compose stop
docker compose up -d
docker compose exec controller workstationctl backup
```

See [Installation](docs/installation.md) for the folder layout and update
procedure.

## Clone and build from source

A GitHub Release is not required for development. Clone the complete
repository and let Docker build the controller, worker, and WSLAN image for the
local Docker platform:

```bash
git clone https://github.com/matthewsawatzky/contain-yourself.git
cd contain-yourself
./dev stack up
```

`./dev` drives everyday development: `./dev check` runs everything CI runs, and
`./dev deploy DIR` points an existing standalone installation at images built
from the checkout, so a release-shaped bug can be reproduced without publishing
anything. Run `./dev help` for the full list.

To build without starting:

```bash
./dev build
```

`DOCKER_DEFAULT_PLATFORM=linux/amd64` or `linux/arm64` may be set when a
specific single-platform development build is needed.

The worker is not published to the host. The controller has no Docker socket
mount. Do not expose port 8080 directly to an untrusted network; put a TLS
reverse proxy, Tailscale, or another authenticated private access layer in
front, then set `SECURE_COOKIES=true`.

## What is included

- SQLite with transactional migrations, foreign keys, WAL, integrity checks,
  operational events, and online backup
- initial administrator setup, PBKDF2 password hashing, server-side hashed
  sessions, session revocation and login rate limiting
- server-rendered dashboard, app launcher, lifecycle controls and live state
  refresh
- per-user accent colour with per-workstation override, contrast-checked
  foregrounds, and a palette endpoint that app UIs can theme against
- strict app manifests and templates with unsafe-field rejection
- pull-request-friendly app-store packages, schemas, deterministic integrity
  metadata, and a generated compact catalogue
- deterministic, labelled Docker resources and an image allowlist
- per-workstation WSLAN with isolated app namespaces, stable service DNS,
  selectable egress (direct, fail-closed WireGuard, host-gateway, IPv6), and
  authenticated ingress
- encrypted WireGuard profiles with global and per-user private catalogues,
  operator-supplied keys, and offline key rotation
- administrator-created user accounts and global profiles visible to new users
- core desktop and terminal definitions plus installable Browser, Code, and
  Files packages
- editable UI-managed template presets that can select any installed app,
  choose an egress mode, and seed the workspace from a chosen image
- bounded persistent app and WSLAN logs under `data/workstation-logs/`
- authenticated HTTP and WebSocket reverse proxy
- expiring, use-limited workstation share links with revocation and scoped
  terminal/app/file/lifecycle permissions
- worker-backed CPU, memory, PID, network and block-I/O samples plus bounded
  per-app logs
- controlled app updates that pull approved images before replacing containers
  while preserving labelled volumes
- non-root volume ownership initialization, reviewed app environment variables,
  and bounded per-app shared memory
- startup reconciliation that reports orphans without silently deleting them
- `workstationctl` JSON API client

## Administrative CLI

Copy the `wm_session` cookie from a logged-in browser into the environment:

```bash
export WORKSTATION_MANAGER_SESSION='...'
workstationctl status
workstationctl list
workstationctl create --name Research --template terminal
workstationctl update ws-abc123def4
workstationctl stop ws-abc123def4
workstationctl backup
workstationctl reconcile
```

## Documentation

- [Architecture](docs/architecture.md)
- [Installation and upgrades](docs/installation.md)
- [GitHub release process](docs/releasing.md)
- [App manifests](docs/app-manifest.md)
- [App store](app_store/README.md)
- [Contributing an app](app_store/docs/contributing.md)
- [Template format](docs/template-format.md)
- [Controller UI design system](docs/ui-design-system.md)
- [Theming and accent colours](docs/theming.md)
- [Networking and reverse proxies](docs/networking.md)
- [Egress modes](docs/egress.md)
- [VPN profiles and multi-user access](docs/vpn-profiles.md)
- [VPN profile encryption key](docs/vpn-encryption-key.md)
- [Authentication](docs/authentication.md)
- [Worker API](docs/worker-api.md)
- [Security model](docs/security-model.md)
- [Recovery](docs/recovery.md)
- [Development](docs/development.md)
- [Troubleshooting](docs/troubleshooting.md)

## Current release boundary

This repository implements a runnable foundation and the primary single-host
flow. Before treating it as a hardened public service, add automated Docker
end-to-end coverage for every chosen third-party app and VPN provider,
controller-enforced file-size policy, finer grained read-only terminal/file
sessions, and a controlled offline restore command. These are recorded
explicitly rather than being hidden behind permissive fallback behavior.

The controller app store supports bounded remote catalogue synchronization,
per-file SHA-256 verification, strict manifest validation, persistent worker
approvals scoped to the complete app specification, atomic activation,
updates, and one-version rollback. The configured HTTPS catalogue is the trust
root; signed catalogue releases are still planned before accepting
third-party publishers without maintainer review.
