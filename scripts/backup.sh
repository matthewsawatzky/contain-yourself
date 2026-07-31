#!/bin/sh
set -eu

docker compose exec controller workstationctl backup "${1:-}"
