#!/bin/sh
set -eu

cd "$(dirname "$0")/.."

if [ ! -f .env ]; then
  if command -v openssl >/dev/null 2>&1; then
    worker_token=$(openssl rand -hex 32)
  else
    worker_token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
  fi
  controller_uid=$(id -u)
  controller_gid=$(id -g)
  sed \
    -e "s/change-this-development-token-0123456789abcdef/$worker_token/" \
    -e "s/^CONTROLLER_UID=.*/CONTROLLER_UID=$controller_uid/" \
    -e "s/^CONTROLLER_GID=.*/CONTROLLER_GID=$controller_gid/" \
    .env.example >.env
  chmod 600 .env
fi
mkdir -p data
docker compose up -d --build
