# Architecture

## Components

```mermaid
flowchart LR
  U["User browser"] -->|"HTTP / WebSocket"| C["Controller :7080"]
  RP["Optional reverse proxy"] --> C
  C --> DB[("SQLite /data/controller.db")]
  C --> VP[("Encrypted WireGuard profiles /data/vpn-profiles")]
  C --> AS[("Verified app packages /data/apps")]
  C -->|"Bounded HTTPS sync"| CAT["Configured app-store index"]
  C -->|"Bearer token; typed requests"| W["Docker worker :8090"]
  W -->|"Unix socket"| D["Docker Engine"]
  C -->|"Authenticated WSLAN ingress"| G["Per-workstation WSLAN gateway"]
  G --> N["Private internal WSLAN bridge"]
  N --- T["Terminal sandbox + app"]
  N --- F["Files sandbox + app"]
  N --- E["Code sandbox + app"]
  N --- B["Browser sandbox + app"]
  T & F & E & B --> V[("Workspace volume")]
```

The Compose management network is shared by the controller, worker and the
management side of each WSLAN gateway. Every workstation also receives its own
Docker `internal` bridge. App sandboxes attach only to that bridge, and each
app uses `container:<sandbox>` network mode. WSLAN is therefore the sole
forwarding path for direct or WireGuard egress.

## Trust and package boundaries

- `internal/httpapi`: HTTP parsing, authorization, rendering and proxy entry.
- `internal/workstations`: explicit state transition rules.
- `internal/database`: all persistence and migrations.
- `internal/manifests` and `internal/templates`: strict configuration scanners.
- `internal/appstore`: bounded catalogue sync, content verification, version
  activation and rollback.
- `internal/workerclient`: only controller-to-worker protocol knowledge.
- `internal/dockerworker`: Docker Engine access and resource validation.
- `pkg/workerapi`: small shared request/response types.

The controller binary cannot access the Docker socket. The worker does not
contain user authentication, UI, SQLite, or a shell/exec endpoint.

Core Desktop and Terminal packages are read-only release configuration.
Optional packages are downloaded into controller data only after the catalogue
index, bundle metadata and every payload hash validate. The worker persists a
second approval for the exact runtime specification before the controller
activates a store package.

## Lifecycle

```text
creating → pulling-images → creating-storage
  → starting-vpn → waiting-for-vpn → starting-apps → ready
  → stopping → stopped
  → deleting → deleted
```

Non-VPN templates skip the two VPN states. Failures become `error`; VPN runtime
failure may be represented as `vpn-failed` or `locked`. Database state updates
use compare-and-swap semantics so concurrent actions cannot silently overwrite
one another.

Creation is asynchronous. The database record and event are committed first,
then the worker pulls approved images, creates labelled volumes and the private
network, starts WSLAN, starts one sandbox per app, and finally starts apps. The
controller only marks the workstation ready after the worker has observed
gateway and app health through WSLAN.

An update follows `ready → pulling-images → creating-storage → … → ready`.
The worker first pulls all allowlisted images, then replaces the WSLAN
gateway, app sandboxes, apps, and private network. Workspace, shell-home and
app-data volumes keep their deterministic labels and survive the rebuild.

## Routing

For controller-path access:

```text
/workstations/ws-abc123def4/apps/terminal/
```

For wildcard DNS, set `PUBLIC_BASE_DOMAIN=workstations.example.com` and forward
both the controller name and `*.workstations.example.com` to port 7080:

```text
ws-abc123def4.workstations.example.com/apps/terminal/
```

The controller resolves the workstation, validates the server-side session and
ownership, checks app installation/readiness, then proxies HTTP or WebSocket
traffic through authenticated WSLAN ingress. App containers never publish host
ports or attach to the management network.

Share traffic uses `/share/<secret>` once, then
`/shared/<workstation-id>/...` with a path-scoped cookie. This routing surface
has no dashboard, logs, metrics or administrative endpoints.

## Reconciliation

At startup the controller compares non-deleted SQLite records with worker
resources labelled `managed-by=workstation-manager`. Missing resource sets mark
records as errors. Orphaned resources are logged and retained for an
administrator; reconciliation never silently deletes volumes.
