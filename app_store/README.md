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
Terminal remain core apps. VPN is a worker-managed system component rather
than an installable app.

The existing top-level `apps/` directory remains the installed catalogue
snapshot during the first migration phase. Store installation will later copy
verified packages into the controller data directory; until then, keep matching
runtime files in both locations in sync.
