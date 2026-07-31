#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
env_file="$root/.env"
compose_file="$root/current/compose.yaml"

die() {
  printf 'workstation-manager: %s\n' "$*" >&2
  exit 1
}

[ -f "$env_file" ] || die "missing $env_file; run the installer again"
[ -f "$compose_file" ] || die "missing current release; run the installer again"

# Values written by the installer are shell-safe double-quoted dotenv values.
set -a
# shellcheck disable=SC1090
. "$env_file"
set +a

dc() {
  docker compose \
    --project-directory "$root" \
    --env-file "$env_file" \
    -f "$compose_file" "$@"
}

case "${1:-help}" in
  start)
    dc pull --policy missing
    dc up -d
    printf 'Workstation Manager: http://%s:8080\n' "${CONTROLLER_BIND:-127.0.0.1}"
    ;;
  stop)
    dc stop
    ;;
  restart)
    dc restart
    ;;
  status)
    dc ps
    ;;
  logs)
    shift
    if [ "$#" -eq 0 ]; then
      set -- controller docker-worker
    fi
    dc logs -f "$@"
    ;;
  backup)
    shift
    dc exec -T controller workstationctl backup "$@"
    ;;
  config)
    dc config
    ;;
  doctor)
    command -v docker >/dev/null 2>&1 || die "Docker is not installed"
    docker info >/dev/null 2>&1 || die "Docker is not running or is inaccessible"
    docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is unavailable"
    [ -d "${DATA_DIRECTORY:?}" ] || die "data directory is missing: $DATA_DIRECTORY"
    [ -d "${CONFIG_DIRECTORY:?}/apps" ] || die "app configuration is missing"
    [ -d "${CONFIG_DIRECTORY:?}/templates" ] || die "template configuration is missing"
    dc config --quiet
    printf 'Installation checks passed (%s).\n' "${WM_VERSION:-unknown version}"
    ;;
  update)
    repository=${WM_GITHUB_REPOSITORY:-}
    [ -n "$repository" ] || die "WM_GITHUB_REPOSITORY is missing from .env"
    installer=$(mktemp "${TMPDIR:-/tmp}/workstation-manager-install.XXXXXX")
    trap 'rm -f "$installer"' EXIT HUP INT TERM
    curl -fL --retry 3 \
      "https://github.com/$repository/releases/latest/download/install.sh" \
      -o "$installer"
    sh "$installer" --repository "$repository" --install-dir "$root"
    ;;
  version)
    cat "$root/current/VERSION"
    ;;
  help|-h|--help)
    cat <<'EOF'
Usage: ./manage COMMAND

Commands:
  start      Pull the configured images and start the services
  stop       Stop the services without deleting data
  restart    Restart the controller and worker
  status     Show Compose service state
  logs       Follow controller and worker logs
  backup     Create a controller SQLite backup
  config     Print the resolved Compose configuration
  doctor     Check Docker, folders, and Compose configuration
  update     Install and start the newest GitHub release
  version    Print the installed release
EOF
    ;;
  *)
    die "unknown command: $1 (run ./manage help)"
    ;;
esac
