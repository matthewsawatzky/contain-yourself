# Installation and upgrades

## Interactive setup

Requirements are Docker Engine 24+ and Docker Compose v2. Start from a
directory you own and can access:

```bash
curl -fsSL https://github.com/matthewsawatzky/contain-yourself/releases/latest/download/bootstrap.sh | sh
```

The bootstrap reads its questions from the terminal even though the script
itself is piped through `sh`. It offers three choices:

1. create `contain-yourself/` inside the current directory (recommended);
2. use the current directory;
3. enter another path.

If the current directory is inaccessible, it safely offers your home
directory instead. It warns before adding files to a non-empty directory.

The bootstrap downloads `compose.yaml`, `setup.sh`, and `update.sh`. The setup
script then performs host-file preparation only. It:

1. downloads the configuration bundle for the exact release embedded in the
   script;
2. verifies the bundle SHA-256 before extraction;
3. creates a random 256-bit controller-to-worker token;
4. records the host UID/GID and local Docker socket;
5. creates `.env`, `config/`, and `data/`;
6. does **not** pull images or start containers.

Review `compose.yaml` and `.env`, then start it yourself:

```bash
cd /the/project/directory
docker compose up -d
```

Compose downloads the exact tagged controller, worker, and WSLAN images from
GHCR. The prebuilt images cover `linux/amd64` and `linux/arm64`, including the
Linux container environments used by Intel and Apple Silicon Docker Desktop.

Open <http://127.0.0.1:7080> and create the first administrator.

## Manual download

To avoid piping a script into a shell, download and inspect the two files
yourself:

```bash
mkdir contain-yourself
cd contain-yourself
curl -fLO https://github.com/matthewsawatzky/contain-yourself/releases/latest/download/compose.yaml
curl -fLO https://github.com/matthewsawatzky/contain-yourself/releases/latest/download/setup.sh
curl -fLO https://github.com/matthewsawatzky/contain-yourself/releases/latest/download/update.sh
chmod +x setup.sh update.sh
./setup.sh
```

When run without `--directory`, `setup.sh` confirms whether its current
directory is the project directory and can create a dedicated subdirectory
instead. Use `./setup.sh --directory /chosen/path` for automation.

## Local layout

Everything associated with the controller is visible in the chosen directory:

```text
contain-yourself/
├── compose.yaml
├── setup.sh
├── update.sh
├── .env
├── config/
│   ├── apps/
│   └── templates/
└── data/
    ├── controller.db
    ├── apps/
    ├── app-store/
    ├── vpn-profiles.key
    ├── vpn-profiles/
    ├── backups/
    ├── workstation-logs/
    │   └── ws-…/
    │       ├── browser.log
    │       ├── network-browser.log
    │       └── wslan.log
    └── worker/
        └── app-approvals.json
```

`config/` contains shipped app definitions and editable template presets.
Templates created in the controller are written to `config/templates/`.
`data/` contains mutable controller and worker state. Per-workstation app,
network sandbox, and WSLAN logs are continuously captured in
`data/workstation-logs/`; each resource keeps a 25 MiB current file and one
rotated previous file. Back up the entire data directory so logs, VPN
ciphertext, and its key remain together.

The setup script preserves an existing `.env`, token, data, and configuration.
Pass `--refresh-config` when you intentionally want to copy newer shipped core
files over files with the same names.

## Operations

Run commands from the directory containing `compose.yaml`:

```bash
docker compose ps
docker compose logs -f controller docker-worker
docker compose stop
docker compose up -d
docker compose exec controller workstationctl backup
```

No application or controller port other than `127.0.0.1:7080` is published by
default.

## Upgrade

The bootstrap installs an updater. By default it prepares files without
changing running containers:

```bash
./update.sh
docker compose up -d
```

Use `./update.sh --apply` to download the release files, pull images, and
recreate changed controller services in one operation. The updater preserves
local settings, data, secrets, and custom templates.

Installations created before v0.4.0 can fetch the updater once:

```bash
curl -fL https://github.com/matthewsawatzky/contain-yourself/releases/latest/download/update.sh -o update.sh
chmod +x update.sh
./update.sh
```

The v0.3 networking upgrade replaces legacy app/VPN namespaces with WSLAN.
Use **Update** once on each workstation created by an older controller. Its
labelled volumes are preserved while runtime containers are replaced.

## Source development

A release is not required for development:

```bash
git clone https://github.com/matthewsawatzky/contain-yourself.git
cd contain-yourself
./scripts/dev-up.sh
```

This builds all images locally. Use the source path for local changes,
unsupported hardware, or private forks.

## Legacy managed installation

`install.sh` remains a compatibility path for installations already managed
under `~/.local/share/workstation-manager`. It no longer starts services unless
passed `--start`. New installations should use the visible Compose-directory
layout above.
