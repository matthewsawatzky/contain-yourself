# App-store roadmap

## Implemented foundation

The controller displays categories, installed/update states, platform
compatibility, image and permission details. Synchronize, install, update and
rollback are administrator actions. Downloads are bounded and verified,
activation is atomic, prior versions are retained, and the worker persists an
approval for the exact app specification.

## Next product features

Updates should show a field-by-field permission diff before approval and never
change a running workstation until its owner chooses Update. Add search,
screenshots, upstream detail views, per-user/request-only availability, signed
catalogue releases and an explicit app uninstall flow.

Useful later additions:

- signed catalogue releases and image provenance/attestation status;
- automated vulnerability, license, architecture, health, base-path, and
  WebSocket test badges;
- stable/beta release channels and per-app changelogs;
- dependencies and conflicts expressed as app IDs and version ranges;
- verified-publisher labels;
- administrator policy for global, per-user, and request-only availability;
- export/import of the approved-app list for offline installations.

Public ratings and reviews are intentionally not a v1 feature. They require
moderation, identity, abuse handling, and a service outside the self-hosted
controller. Curated quality and transparent CI results are more useful first.

## App lineup

Core and always available:

- Desktop — controller-native launcher, not a third-party image
- Terminal — minimum useful workstation tool
- VPN — worker-managed networking component, not an app package

Initial official catalogue:

- Browser
- Code
- Files

Good candidates after image, license, multi-architecture, base-path, and
security testing:

- JupyterLab for notebooks and data work
- a lightweight Git web client
- a database client such as CloudBeaver
- a self-hosted notes/editor app
- a media player suitable for files in the shared workspace

Docker administration tools are poor default-store candidates because they
normally need the Docker socket or elevated container permissions. A video
player should not be published until its browser streaming, codecs, image
architectures, and workspace-only file access are verified.
