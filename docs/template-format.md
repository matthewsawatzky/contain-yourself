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

The dashboard lets a user deselect apps from the chosen template. It never lets
the request add an app that the template did not approve.

VPN-required templates also require an enabled WireGuard profile accessible to
the workstation owner. That can be an administrator-created global profile or
the user's private profile. `developer-local` and `browser-local` are explicit
direct-internet variants for installations that do not require VPN routing.
