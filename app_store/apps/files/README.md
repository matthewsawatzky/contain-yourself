# Files

A purpose-built browser for the workstation's shared workspace volume.

Browse folders, upload and download, and preview images, video, audio, and
plain text in place. Video seeking works because the server honours HTTP range
requests.

## Why not a general-purpose file manager

Two properties are easier to guarantee in a few hundred lines than to verify in
a third-party image:

- **Confinement.** Every path is resolved through `os.Root`, so a request
  cannot leave the workspace directory even by following a symlink placed
  inside it.
- **No inline execution.** App traffic is proxied on the controller's own
  origin. Only images, video, audio, and plain text are served inline; anything
  else — including HTML and SVG, both of which can carry script — is sent as an
  attachment with `Content-Security-Policy: sandbox`.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `FILES_ROOT` | `/workspace` | Directory to serve |
| `FILES_BASE_PATH` | `/apps/files` | Prefix the controller proxies under |
| `FILES_MAX_UPLOAD_BYTES` | `5368709120` | Upload size cap |
| `FILES_LISTEN` | `0.0.0.0:7080` | Listen address |

The app follows the controller's accent colour by reading `/api/v1/theme` at
load, and falls back to its own palette if that is unavailable.
