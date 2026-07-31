# Installation and upgrades

## One command

Each tagged GitHub release contains a repository-specific installer:

```bash
curl -fsSL https://github.com/matthewsawatzky/contain-yourself/releases/latest/download/install.sh | sh
```

It requires Docker Engine and Docker Compose v2. It does not require Git, Go,
or a source checkout. The installer:

1. downloads the latest release bundle and checksum from GitHub Releases;
2. verifies SHA-256 before extraction;
3. creates a random 256-bit controller-to-worker token;
4. records the host UID/GID for Linux bind-mount permissions;
5. selects versioned controller and worker images from GHCR;
6. creates the config/data layout and starts Compose.

The prebuilt controller and worker cover `linux/amd64` and `linux/arm64`.
Those are also the Linux-container platforms used by Intel/Apple Silicon
Docker Desktop and the usual Windows Docker Desktop/WSL2 installations. Every
image in the default app catalogue currently publishes both architectures.

The repository and its GHCR packages must be public for anonymous
installation. Private deployments can authenticate Docker and GitHub download
requests separately before running the installer.

## Installed layout

The default location is `~/.local/share/workstation-manager`:

```text
workstation-manager/
├── .env
├── manage
├── config -> current/config
├── data/
│   ├── controller.db
│   ├── vpn-profiles.key
│   ├── vpn-profiles/
│   └── backups/
├── current -> releases/v1.2.3
└── releases/
    └── v1.2.3/
        ├── VERSION
        ├── compose.yaml
        └── config/
            ├── apps/
            └── templates/
```

`data/` is never replaced by installation or upgrade. `config/` points to the
reviewed catalogue shipped with the current release. To maintain a custom
catalogue, copy it elsewhere and set `CONFIG_DIRECTORY` in `.env` to that
directory; upgrades preserve this setting.

The installer also creates `wm` and `workstation-manager` links under
`~/.local/bin`. If that directory is not on `PATH`, invoke the full
`~/.local/share/workstation-manager/manage` path or update the shell path.

## Operations

```bash
wm doctor
wm status
wm logs
wm backup
wm restart
wm stop
wm start
```

`wm update` downloads the latest release installer from the configured GitHub
repository. It installs the new release beside the old one, atomically switches
`current`, preserves `.env` and `data/`, pulls the exact new image tag, and
recreates the services. Previous release folders remain available for
inspection and a future rollback command.

Edit `.env` to change the bind address, external base domain, secure-cookie
mode, session lifetime, Docker socket source, data path, or config path.

## Development startup

From a source checkout:

```bash
./scripts/dev-up.sh
```

That is also one command, but it builds images locally. End-user installation
uses GitHub release assets and prebuilt GHCR images instead.

Use the source path for unsupported hardware, local patches, or private forks.
The controller and worker may build on additional Go/Alpine architectures, but
the usable app set is limited to third-party images published for that
architecture.
