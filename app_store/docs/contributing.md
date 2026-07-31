# Contributing an app

For apps that expose a web interface, see the
[controller UI design system](../../docs/ui-design-system.md) for shared
colors, spacing, control states, icon guidance, and base-path requirements.

Contributions are normal pull requests. Add one directory beneath
`app_store/apps` and do not change generated files by hand.

Required files:

- `app.yaml`: strict runtime manifest
- `bundle.json`: store description, attribution, compatibility, and generated
  payload integrity data
- `icon.svg` or `icon.png`: local icon with a permissive redistribution license
- `README.md`: setup notes, upstream links, limitations, and test instructions

Before opening the pull request:

```bash
go run ./cmd/storectl build
go test ./...
go vet ./...
git diff --check
```

The pull request must explain:

- why the app is useful in a remote workstation;
- which architectures were tested;
- whether it works below a URL base path and with WebSockets;
- what persistent storage it requests;
- why every capability or environment variable is necessary;
- the upstream image source and license;
- how a reviewer can perform a basic health and launch test.

## Acceptance policy

An app must use an explicit image version, work without host mounts or the
Docker socket, expose one unique unprivileged HTTP port, pass through the
controller proxy, and declare only permissions accepted by the core manifest
validator. Images should support both `linux/amd64` and `linux/arm64`; a
single-platform app must say so clearly.

Applications that require privileged mode, host networking, host PID,
arbitrary devices, `SYS_ADMIN`, or direct Docker access are not accepted.
Container-management dashboards therefore do not belong in the default
catalogue.

Maintainers review the runtime manifest and image provenance as executable
code. A valid schema is necessary, but it is not security approval.
