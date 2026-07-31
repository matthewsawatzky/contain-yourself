# Code

Browser-accessible code-server with the workstation mounted at `/workspace`.
Editor state is stored in a separate per-app volume.

Authentication is intentionally delegated to the Contain Yourself controller;
the app port is never published directly.
