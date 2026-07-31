# Development

Requires Go 1.24+, Docker, and Compose.

```bash
go test ./...
go vet ./...
docker compose --env-file .env config --quiet
./scripts/dev-up.sh
```

`dev-up.sh` creates `.env`, generates a worker token, records the current host
UID/GID for writable bind mounts, creates `data/`, builds all three local images
(controller, worker, and WSLAN),
and starts Compose.

No GitHub Release is involved in this workflow. A normal branch push contains
the entire buildable project. From a fresh clone:

```bash
./scripts/dev-build.sh  # build only
./scripts/dev-up.sh     # build and run
```

Both use the current Docker platform. To request a different platform from a
capable BuildKit installation:

```bash
DOCKER_DEFAULT_PLATFORM=linux/arm64 ./scripts/dev-build.sh
```

Useful targets:

```bash
make fmt
make test
make vet
make compose-config
make logs
```

Package responsibilities are documented in
[architecture.md](architecture.md). Keep HTTP handlers thin, state transitions
in `internal/workstations`, database writes transactional, and all Docker
details under `internal/dockerworker`.

## Adding a migration

Append an immutable migration string in `internal/database/database.go`, add a
matching numbered reference file in `migrations/`, and test both a fresh
database and an upgrade fixture. Never edit a released migration.

## Adding an app

1. Add `app_store/apps/<id>/app.yaml`, `bundle.json`, README, and local icon.
2. Pin a version tag or digest.
3. Run `make store` to generate hashes and the compact index.
4. Add manifest rejection and Docker integration tests.
5. Verify base-path behavior, WebSockets, health, shared workspace and VPN
   egress before enabling it.
6. Run `make store-check` and follow the contribution checklist.

Only Desktop and Terminal belong in `core_apps/`. Store installation creates a
persistent worker approval for the complete validated app specification;
community images do not need to be added to the static core allowlist.

## Adding a template

Add core presets to `core_templates/<id>.yaml`. Optional examples may live in
`example_templates/`. Only select valid registered apps. Resource defaults
must fit the host and every app's individual limits must fit the workstation.

## Test tiers

- Unit: manifests, templates, state, sessions, shares, hostname, log decoding
  and worker validation.
- Docker integration: pull/provision/health/proxy/persistence/kill switch.
- End to end: setup, create, app interaction, stop/start, share and delete.

Docker tests should use a separate daemon or uniquely labelled resources and
must always clean up their exact IDs.

## Source and deployment layout

- `cmd/`, `internal/`, `pkg/`, `web/`, `migrations/`: Go application source.
- `core_apps/`, `core_templates/`: minimal definitions shipped in releases.

Controller pages and app-facing visual conventions are documented in
[Controller UI design system](ui-design-system.md).
- `app_store/`: optional packages, schemas, contribution docs and index.
- `build/`: development and release image definitions.
- `deploy/`: production Compose file using versioned GHCR images.
- `scripts/`: development build/startup, installer, management, backup, and
  release bundle tooling.
- `.github/`: CI, multi-architecture image publishing, and GitHub Releases.

Build a local release asset without publishing:

```bash
make release VERSION=v0.3.0
```
