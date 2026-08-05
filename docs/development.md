# Development

Requires Go 1.24+, Docker, and Compose.

`./dev` is the entry point for everyday work. It builds and runs everything
from the checkout, so nothing needs to be tagged, pushed, or downloaded.

```bash
./dev doctor      # check the toolchain and this checkout
./dev stack up    # build the three images and start the stack
./dev stack logs  # follow the controller and worker
./dev check       # everything CI runs, before you push
```

Run `./dev help` for the full list.

## Testing a release-shaped install

The repository's own `compose.yaml` builds from source, but a standalone
installation created by `bootstrap.sh` runs published images from GHCR. To
reproduce a bug in that shape without cutting a release:

```bash
./dev deploy ~/path/to/contain-yourself --restart
```

That builds `contain-yourself-local-{controller,worker,wslan}:dev` from the
checkout, repoints the installation's `.env` at them (keeping a
`.env.dev-backup`), and copies `core_apps/` and `core_templates/` into the
installation's `config/`.

That last step matters more than it looks. An installation mounts its own copy
of the core app and template configuration, so a manifest edit in this
repository has no effect on it until the files are synced — a rebuilt image
alone will not do it.

To go back to published images, restore `.env.dev-backup`, or set
`WM_IMAGE_REPOSITORY` and `WM_VERSION` back to their release values.

## Underlying commands

`./dev` wraps these; they still work directly.

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
5. Run `./dev app test <id>` (needs Docker). It provisions the app through the
   real WSLAN gateway and per-app network sandbox with its actual image,
   command and environment -- the same topology the worker uses in
   production -- and checks the declared health path through WSLAN ingress.
   A pass is evidence the base path, `strip_prefix`, health path and shared
   workspace mount are all consistent, without needing a full stack or a
   manual walkthrough.
6. Run `make store-check` and follow the contribution checklist.

`./dev app test` with no ID runs every container-service app in `core_apps/`
and `app_store/apps/`. `./dev app interop` checks the WSLAN property the whole
catalog depends on: two unmodified apps sharing a network (as a template
combining several apps does) route correctly through the gateway even when
they share an internal port, and can reach each other directly by DNS alias
with neither one aware the other exists. Neither command modifies an app's
image or manifest to make it pass -- if it needs that, the app or WSLAN itself
has a bug worth filing.

Only Desktop and Terminal belong in `core_apps/`. Store installation creates a
persistent worker approval for the complete validated app specification;
community images do not need to be added to the static core allowlist.

## Adding a template

Add core presets to `core_templates/<id>.yaml`. A core template loads before
any store app is installed, so it can only select apps in `core_apps/`
(currently `terminal` and `web-desktop`). A template that combines store
apps -- the common case, since most useful combinations pull in `browser`,
`code`, or `files` -- belongs in `example_templates/` instead, alongside the
existing `browser.yaml` and `developer.yaml`. Either way, only select valid
registered apps; resource defaults must fit the host and every app's
individual limits must fit the workstation.

## Test tiers

- Unit: manifests, templates, state, sessions, shares, hostname, log decoding
  and worker validation.
- Docker integration: pull/provision/health/proxy/persistence/kill switch, plus
  `./dev app test [id]` and `./dev app interop` for the WSLAN/app boundary
  specifically (see "Adding an app" above).
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
