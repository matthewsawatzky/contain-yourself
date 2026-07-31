# App manifest

Each app lives in `apps/<id>/app.yaml`. The scanner uses strict YAML decoding:
unknown fields are errors rather than ignored options. A malformed package
remains visible as a validation error and cannot be selected by a template.

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

An image must also appear in `WORKER_ALLOWED_IMAGES`. Manifest validation and
worker validation are intentionally duplicated across the trust boundary.

After changing packages, call:

```bash
workstationctl apps list
workstationctl templates list
# Administrator API:
curl -X POST -b wm_session=... -H 'Origin: http://127.0.0.1:8080' \
  http://127.0.0.1:8080/api/v1/admin/rescan
```
