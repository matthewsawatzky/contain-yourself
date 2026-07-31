# Repository and publishing design

The store begins inside the main repository so schema, controller, examples,
and CI can change in one pull request. Its directory is deliberately
self-contained.

Move it to a dedicated `contain-yourself-app-store` repository when catalogue
releases need to happen independently or community review permissions should
be separated from core code. A separate store repository also avoids mixing
controller releases with frequent app updates. Keep the schema version and
minimum controller version fields when splitting it.

The controller installation flow is:

1. Fetch a bounded `index.json` over HTTPS with conditional requests and retain
   the last known good copy.
2. Display metadata without pulling images or installing files.
3. Let an administrator review image, storage, network, and capabilities.
4. Fetch `bundle.json` and package files, enforcing size limits and verifying
   every SHA-256.
5. Strictly validate `app.yaml`. The worker independently accepts the image
   only if it is already in the host allowlist or the app approval carries a
   store signature the worker can verify. A controller-provided image string
   alone never expands the worker allowlist.
6. Archive the version under `/data/app-store/versions/<id>/<version>`,
   atomically activate it at `/data/apps/<id>`, then rescan the registry.
7. Pin the installed version and keep the previous version for rollback.

The first implementation trusts the configured HTTPS catalogue origin and
verifies all content against its index. Published third-party catalogues should
add a signed index before they are accepted without maintainer review.

Core packages live in read-only `/config/apps`; store-installed packages live
in `/data/apps`. Core IDs cannot be overridden, and duplicate IDs across
sources fail rather than being resolved by filesystem order.
