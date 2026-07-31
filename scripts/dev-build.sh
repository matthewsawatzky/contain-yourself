#!/bin/sh
set -eu

cd "$(dirname "$0")/.."

# Compose validates required interpolation even though the token is not used by
# image builds. This value never enters either image.
WORKER_TOKEN=${WORKER_TOKEN:-build-only-token-not-used-at-runtime}
export WORKER_TOKEN

docker compose build "$@"
