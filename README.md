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

## One-command installation

Requirements: Docker Engine 24+ with Compose v2, Linux host support for
`/dev/net/tun` when using VPN workstations, and enough disk for selected images.

After the repository has published its first GitHub release, installation is:

```bash
curl -fsSL https://github.com/matthewsawatzky/contain-yourself/releases/latest/download/install.sh | sh
```

The release workflow bakes the repository identity into the downloaded
installer, so users do not pass repository names, tokens, paths, or image
tags. The installer verifies the release bundle, generates the worker token,
creates the installation folders, pulls the controller and worker from GitHub
Container Registry, and starts Compose.

Open <http://127.0.0.1:8080> and create the initial administrator. The
Terminal and Private workstation templates can be used immediately. Install
optional Browser, Code, and Files applications from **App store**. Before
launching a Private workstation, open **VPN profiles** and paste a WireGuard
configuration from your VPN provider. No VPN account credentials belong in
`.env`.

Common management commands are:

```bash
wm status
wm logs
wm backup
wm update
wm stop
wm start
```

The default installation is under
`~/.local/share/workstation-manager`. Add `~/.local/bin` to `PATH` if the
shell does not already include it. See [Installation](docs/installation.md)
for the folder layout, custom paths, and manual development startup.

## Clone and build from source

A GitHub Release is not required for development. Clone the complete
repository and let Docker build the controller and worker for the local Docker
platform:

```bash
git clone https://github.com/matthewsawatzky/contain-yourself.git
cd contain-yourself
./scripts/dev-up.sh
```

To build without starting:

```bash
./scripts/dev-build.sh
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
- strict app manifests and templates with unsafe-field rejection
- pull-request-friendly app-store packages, schemas, deterministic integrity
  metadata, and a generated compact catalogue
- deterministic, labelled Docker resources and an image allowlist
- per-workstation WSLAN with isolated app namespaces, stable service DNS,
  direct or fail-closed WireGuard egress, and authenticated ingress
- encrypted WireGuard profiles with global and per-user private catalogues
- administrator-created user accounts and global profiles visible to new users
- core desktop and terminal definitions plus installable Browser, Code, and
  Files packages
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
- [Networking and reverse proxies](docs/networking.md)
- [VPN profiles and multi-user access](docs/vpn-profiles.md)
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
