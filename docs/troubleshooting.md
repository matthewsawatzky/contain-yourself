# Troubleshooting

## Stack does not become healthy

```bash
docker compose ps
docker compose logs controller docker-worker
docker info
```

The worker must see `/var/run/docker.sock`. Rootless Docker uses a different
socket; update the bind mount and `DOCKER_SOCKET` together.

## VPN template enters error

Open **VPN profiles**, confirm the selected profile is enabled and accessible
to that user, and verify the provider's WireGuard configuration uses an IP
address (not a hostname) in `Endpoint`. Confirm `/dev/net/tun` exists on the
Linux host. Inspect:

```bash
docker logs wm-ws-XXXXXXXXXX-vpn
docker inspect --format '{{json .State.Health}}' wm-ws-XXXXXXXXXX-vpn
```

Applications are not started until the gateway is healthy. Do not bypass this
check by attaching them to another network.

## Image pull rejected

The image must match both `apps/<id>/app.yaml` and
`WORKER_ALLOWED_IMAGES` exactly. Tags are part of the value. An allowlist error
is different from a registry authentication or unavailable-tag error.

## App health timeout

Inspect the labelled container and its logs:

```bash
docker ps -a --filter label=workstation-id=ws-XXXXXXXXXX
docker logs wm-ws-XXXXXXXXXX-app-terminal
```

Check the manifest's internal port and health path. The app must listen on
`0.0.0.0`, not loopback.

## WebSocket terminal disconnects

The external proxy must forward `Upgrade` and `Connection`, preserve the Host
header, avoid buffering, and use a long read timeout. Do not strip the app base
path unless its manifest requests it.

## Wildcard host shows the dashboard

Set `PUBLIC_BASE_DOMAIN` without a scheme or wildcard, for example
`workstations.example.com`. Ensure wildcard DNS and the proxy preserve the
original Host header.

## Database errors

The release installer runs the controller as the installing user's UID/GID so
the linked `DATA_DIRECTORY` remains writable and inspectable on Linux. A manual
source installation should set `CONTROLLER_UID=$(id -u)` and
`CONTROLLER_GID=$(id -g)` in `.env`. If using the image's built-in UID 10001,
set ownership explicitly:

```bash
mkdir -p data
sudo chown 10001:10001 data
```

For corruption messages, stop immediately and follow
[recovery.md](recovery.md).
