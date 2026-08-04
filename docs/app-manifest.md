# App manifest

Core apps live in `core_apps/<id>/app.yaml`; store apps live in
`app_store/apps/<id>/app.yaml`. The scanner uses strict YAML decoding:
unknown fields are errors rather than ignored options. A malformed package
remains visible as a validation error and cannot be selected by a template.

An optional app's adjacent `bundle.json` contains store copy,
attribution, compatibility and integrity metadata; it does not duplicate
runtime permissions. See [the app-store format](../app_store/docs/bundle-format.md).

## Schema

```yaml
schema_version: 1
id: terminal                 # ^[a-z][a-z0-9-]{1,31}$
name: Terminal
version: 1.0.0
description: Browser terminal
runtime:
  type: container-service    # controller-ui | container-service | workspace-image
  image: vendor/image:1.2.3  # explicit, non-latest tag
  command: ["server"]
  environment:              # fixed, reviewed keys only
    TZ: Etc/UTC
  internal_port: 7681        # 1024–65535
routing:
  base_path: /apps/terminal/
  strip_prefix: false
  websocket: true
network:
  mode: workstation-vpn      # workstation-vpn | management-only
storage:
  - type: workspace          # workspace | app-data | shell-home | temporary
    target: /workspace       # absolute container path
    owner_uid: 1000          # optional, must be paired with owner_gid
    owner_gid: 1000
resources:
  default_memory_mb: 512
  default_cpu: 0.5
  shm_size_mb: 256           # optional, capped at 2048 and app memory
health:
  type: http                 # http | tcp
  path: /
  timeout_seconds: 5
permissions:
  capabilities: [NET_RAW]
desktop:
  visible: true
  icon: icon.svg
  default_width: 1000
  default_height: 650
  singleton: false
```

`controller-ui` apps cannot declare an image, command or port.

## Health paths and prefixes

`health.path` is the path the **app itself** serves. The worker's probe reaches
the app through WSLAN directly and does not apply the controller proxy's prefix
handling, so it does not match `routing.base_path` automatically.

| `strip_prefix` | Where the app serves | `health.path` must be |
| --- | --- | --- |
| `true` | the root — the proxy removes the prefix | a root path, e.g. `/` or `/healthz` |
| `false` | under `base_path` — the app is prefix-aware | inside `base_path`, e.g. `/apps/terminal/` |

Validation rejects a `health.path` outside `base_path` when `strip_prefix` is
false. Without that check the failure is slow and misleading: the app starts
normally, every probe returns 404, and provisioning fails 90 seconds later with
a timeout that reads as though the app never came up.

An app that is prefix-aware usually has a launch flag that says so — ttyd's
`--base-path`, for example. If you set one, the health path has to include it.

## Desktop roles

`desktop.role` tells the controller what part an app plays in the workstation
shell. It is optional, and most apps leave it unset.

| Role | Required runtime | Meaning |
| --- | --- | --- |
| unset | any | An ordinary app. Appears as a launcher tile. |
| `launcher` | `controller-ui` | Renders the controller's app launcher. Never listed as a tile on itself, and always reachable through a share because it only lists apps the share already grants. |
| `desktop` | `container-service` | A full graphical desktop, such as a VNC or RDP session, in place of the launcher. Requires the `open-apps` share permission like any other app. |

The role, not the app id, drives launcher rendering and share authorization, so
the bundled `web-desktop` launcher can be replaced by another package without
patching the controller. A `launcher` role on a container app is rejected: that
would place an untrusted container where the trusted controller page belongs.

Apps with `desktop.visible: false` are omitted from the launcher.
`container-service` routing must begin with `/apps/<id>`. Icon paths must be
relative and remain inside the package.

## Security restrictions

The schema has no fields for privileged mode, host networking, host PID, host
paths, devices, Docker socket mounts, seccomp disabling, host commands or env
files. Runtime environment variables are limited to a small reviewed set used
by packaged GUI apps; newline-bearing values and unknown keys are rejected.
Adding any other YAML key fails strict decoding. The only app-added
capability currently accepted is `NET_RAW`; the VPN gateway is a separate
worker-owned resource with its own fixed `NET_ADMIN` policy.

When non-root images need fresh volumes, `owner_uid` and `owner_gid` ask the
worker to run a short, fixed `chown` initializer using that already-approved
image. The initializer has no network, is deleted immediately, and is not
available through an execution API.

Core images may appear in `WORKER_ALLOWED_IMAGES`. Store installation sends
the worker the complete validated app specification; its persistent approval
is scoped to the app ID, version, manifest hash, image and every runtime field.
Changing a command, mount, environment value, resource limit or image
invalidates the approval. Manifest validation and worker validation remain
duplicated across the trust boundary.

After changing packages, call:

```bash
workstationctl apps list
workstationctl templates list
# Administrator API:
curl -X POST -b wm_session=... -H 'Origin: http://127.0.0.1:7080' \
  http://127.0.0.1:7080/api/v1/admin/rescan
```
