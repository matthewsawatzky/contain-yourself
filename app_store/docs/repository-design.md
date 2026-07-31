# Repository and publishing design

The store begins inside the main repository so schema, controller, examples,
and CI can change in one pull request. Its directory is deliberately
self-contained.

Move it to a dedicated `contain-yourself-app-store` repository when catalogue
releases need to happen independently or community review permissions should
be separated from core code. A separate store repository also avoids mixing
controller releases with frequent app updates. Keep the schema version and
minimum controller version fields when splitting it.

The controller installation design is:

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
6. Copy the package atomically into
   `/data/apps/<id>/<version>`, then rescan the registry.
7. Pin the installed version and keep the previous version for rollback.

Published catalogues should use immutable release assets or a signed index.
Installing directly from an unpinned mutable branch is unsuitable for the
trusted default store.

Local administrators will also be able to provide packages in `/config/apps`.
Core IDs cannot be overridden, and duplicate IDs across local and store
sources must fail visibly rather than being resolved by filesystem order.
