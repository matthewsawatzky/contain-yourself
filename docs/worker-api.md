# Worker API

The worker listens on port 8090 only inside the Compose network. Every `/v1`
request requires:

```text
Authorization: Bearer <WORKER_TOKEN>
Content-Type: application/json
```

The token is compared in constant time. JSON is capped at 1 MiB, rejects
unknown fields, and accepts exactly one value.

| Method | Route | Purpose |
|---|---|---|
| `GET` | `/healthz` | Docker socket readiness |
| `GET` | `/v1/resources` | list labelled managed containers |
| `GET` | `/v1/workstations/{id}` | inspect one resource set |
| `GET` | `/v1/workstations/{id}/usage` | bounded one-shot Docker stats |
| `GET` | `/v1/workstations/{id}/apps/{app}/logs` | bounded recent logs |
| `POST` | `/v1/apps/approve` | persist approval for one exact validated app specification |
| `POST` | `/v1/workstations` | provision reviewed apps and storage |
| `POST` | `/v1/workstations/{id}/rebuild` | pull and replace containers, retain volumes |
| `POST` | `/v1/workstations/{id}/action` | start, stop, restart or delete |

There is no shell exec endpoint, arbitrary bind mount, arbitrary device,
privileged flag, host network mode, host PID mode, or generic Docker passthrough.

The worker validates IDs, image allowlists, CPU/memory/PID bounds, unique ports,
storage types, absolute targets and bounded ownership, health paths, reviewed
environment keys, shared-memory bounds, and capabilities. It creates
deterministic names and these labels:

```text
managed-by=workstation-manager
workstation-id=ws-abc123def4
resource-type=app
app-id=terminal
```

Deletion resolves exact labels, removes containers first, then only volumes
with the same workstation labels.

Logs are capped at 1,000 requested lines and 2 MiB of worker response data.
Stats expose CPU, memory, PIDs, network and block I/O for managed containers
only. Rebuild validates the complete provision request and pulls every image
before deleting apps, sandboxes, WSLAN, or the private network.

VPN provision requests contain the selected canonical WireGuard configuration.
It crosses only the authenticated internal controller-to-worker connection.
The worker validates it again, creates the repository-built WSLAN gateway, and
uploads it as the mode-`0600` `/run/wslan/wg0.conf` before container start.
The private key is therefore absent from the container environment and app
containers do not share the gateway filesystem. There is no generic file
upload or arbitrary Docker archive endpoint.
