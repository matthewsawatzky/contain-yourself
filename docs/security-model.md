# Security model

## Trust boundaries

1. Browser input is untrusted. The controller authenticates and authorizes it.
2. App/template files are administrator-controlled but still strictly parsed.
3. Controller-to-worker requests cross an internal bearer-token boundary.
4. The Docker worker is highly privileged because the Docker socket is
   effectively host-root. Compromise of the worker must be treated as host
   compromise.
5. Workstation containers and their content are untrusted.

## Defenses

- Controller has no Docker socket, runs as the configured non-root host
  UID/GID (the image default is 10001), and uses `no-new-privileges`.
- Worker publishes no host port and offers no exec or generic create API.
- Images require two approvals: a valid manifest and the worker allowlist.
- Host mounts, privileged containers, host namespaces, arbitrary devices and
  arbitrary capabilities are absent from the protocol.
- VPN apps share only the Gluetun namespace, giving them no fallback host route.
- Uploaded WireGuard profiles are strictly parsed, encrypted at rest, and
  injected as a file before the VPN container starts rather than exposed in
  its environment.
- App ports are not published; all user traffic passes through controller
  sessions and ownership checks.
- SQLite uses foreign keys, WAL, one writer connection, transactional
  migrations and startup integrity checking.
- Managed resources use deterministic names and immutable ownership labels.
- Share secrets are stored hashed, redeemed with transactional use limits, and
  converted to workstation-path-scoped cookies.
- Update pulls all approved images before removing existing containers and
  preserves only worker-labelled volumes.

## Threats and limitations

The worker still controls Docker and must remain reachable only from the
management network. Docker's Unix socket is not a security sandbox.

Third-party app images execute code and should be mirrored, scanned and pinned
by digest for production. Version tags are immutable by policy, not by Docker.
The included tags are examples that require deployment-specific validation.

The management network is not marked `internal` because the worker must pull
images and the controller may perform management checks. It must not be
attached to unrelated containers.

An `open-apps` share grants the interaction model exposed by each selected
third-party application; the controller cannot reliably turn a code editor
into a read-only editor. File upload/download distinctions are enforced at the
HTTP method boundary for the file app, but application-specific APIs still
require deployment testing. Use separate low-trust workstations for untrusted
recipients.

TLS is deliberately external. `SECURE_COOKIES=false` exists only for loopback
quick start.

The generated VPN encryption key is stored beside the encrypted profiles in
the controller data directory. This prevents routine plaintext exposure and
private keys appearing in SQLite or Docker inspection, but it does not protect
profiles from a host compromise or from theft of a backup containing both
files.
