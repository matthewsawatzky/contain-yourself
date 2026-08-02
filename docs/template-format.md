# Template format

Core templates are presets in `core_templates/*.yaml`; they all use the same
lifecycle. Optional examples live in `example_templates/` and are valid only
after their referenced store apps are installed.

```yaml
schema_version: 1
id: developer
name: Developer
description: Development workstation
workspace_image: alpine:3.21
apps: [web-desktop, terminal, files, code]
vpn_required: true
persistent: true
cpu: 2
memory_mb: 4096
pid_limit: 512
expires_hours: 0
```

IDs are lowercase letters, digits and hyphens. Images must be pinned and every
app must resolve to a valid scanned manifest. Resource minimums are 128 MB
memory and 32 PIDs. `expires_hours: 0` means no automatic expiration.

## Workspace images

`workspace_image` seeds the shared workspace volume that every app in the
workstation mounts. On first creation the worker runs the image once, copies
`/opt/workspace-seed/.` into `/workspace` if that directory exists, writes a
`/workspace/.workstation-seeded` marker, and removes the container. It is not a
long-lived container and apps do not run inside it.

Seeding happens once per volume. A workstation update or rebuild re-runs the
seed container, sees the marker, and exits without touching files the user has
changed since. To re-seed, delete the workstation so its volumes go with it.

The image must be pinned and must appear in the worker's
`WORKER_ALLOWED_IMAGES` allowlist, the same gate app images pass. An image
without `/opt/workspace-seed` is valid and simply produces an empty workspace,
so a plain base image such as `alpine:3.21` is a reasonable default. Use a
larger image when a workstation should start with toolchains, dotfiles, or
sample data already in place.

Templates are presets rather than security allowlists. Their `apps` field
defines the checkboxes selected by default. At launch, a user may select any
installed app that passed manifest validation and administrator approval.
This means installing Browser in the app store makes it immediately available
with both built-in and custom templates.

Administrators can create and update templates at **Templates** in the
controller. UI-created IDs are prefixed with `custom-` and are stored as
ordinary YAML files under `config/templates/`. Those files can be reviewed,
versioned, or edited by hand. Built-in template IDs cannot be overwritten or
deleted through the UI.

VPN-required templates also require an enabled WireGuard profile accessible to
the workstation owner. That can be an administrator-created global profile or
the user's private profile. `developer-local` and `browser-local` are explicit
direct-internet variants for installations that do not require VPN routing.
