# Contain Yourself app store

This directory is the source catalogue for optional Contain Yourself apps. It
is intentionally self-contained so it can later be moved to a dedicated
repository without changing the package format.

The controller reads the compact generated `index.json` to list apps. Each
entry points to an app's strict `bundle.json`; the runtime definition remains
`app.yaml`. Keeping those jobs separate prevents store copy and container
permissions from drifting apart.

## Layout

```text
app_store/
├── index.json
├── schemas/
│   ├── bundle.schema.json
│   └── index.schema.json
├── docs/
│   ├── bundle-format.md
│   ├── contributing.md
│   ├── repository-design.md
│   └── roadmap.md
└── apps/
    └── <app-id>/
        ├── app.yaml
        ├── bundle.json
        ├── icon.svg
        └── README.md
```

Validate every package and rebuild the deterministic index with:

```bash
go run ./cmd/storectl build
go run ./cmd/storectl check
```

The initial optional catalogue contains Browser, Code, and Files. Desktop and
Terminal remain in `core_apps/`. VPN is a worker-managed system component
rather than an installable app.

The controller synchronizes the generated index, verifies `bundle.json` and
every payload file, asks the worker to approve the exact app specification,
then atomically activates the package under `/data/apps`.

## Controller workflow

1. An administrator opens **App store** and selects **Synchronize catalogue**.
2. Every signed-in user may browse the cached catalogue and permission
   summary; only administrators may install, update, or roll back.
3. An installed app becomes globally available in the workstation creation
   form. Installing it does not silently add it to existing workstations.
4. An app update or rollback changes the active definition for future
   provisioning. Owners explicitly select **Update** on an existing
   workstation before its containers are rebuilt with that definition.

The controller retains the last valid catalogue if refresh fails. It retains
the prior installed package after an update, enabling one-click rollback. App
uninstall and per-user app availability are later lifecycle features.
