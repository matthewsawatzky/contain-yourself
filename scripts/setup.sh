#!/bin/sh
set -eu

default_repository='@GITHUB_REPOSITORY@'
default_version='@RELEASE_VERSION@'
repository=${WM_GITHUB_REPOSITORY:-$default_repository}
version=${WM_VERSION:-$default_version}
project_directory=
local_bundle=
refresh_config=false
assume_yes=false

die() {
  printf 'contain-yourself setup: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage: ./setup.sh [options]

Prepares a user-owned Compose directory without starting Docker.

Options:
  --directory PATH       Directory to prepare
  --repository OWNER/REPO
  --version vMAJOR.MINOR.PATCH
  --bundle PATH          Use a local release bundle for testing
  --refresh-config       Refresh shipped core app/template files
  -y, --yes              Use the script directory without prompting
  -h, --help             Show this help
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --directory)
      [ "$#" -ge 2 ] || die "--directory requires a value"
      project_directory=$2
      shift 2
      ;;
    --repository)
      [ "$#" -ge 2 ] || die "--repository requires a value"
      repository=$2
      shift 2
      ;;
    --version)
      [ "$#" -ge 2 ] || die "--version requires a value"
      version=$2
      shift 2
      ;;
    --bundle)
      [ "$#" -ge 2 ] || die "--bundle requires a value"
      local_bundle=$2
      shift 2
      ;;
    --refresh-config)
      refresh_config=true
      shift
      ;;
    -y|--yes)
      assume_yes=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown option: $1"
      ;;
  esac
done

if [ -n "$local_bundle" ]; then
  case "$local_bundle" in
    /*) ;;
    *)
      caller_directory=$(pwd -P 2>/dev/null) ||
        die "a relative --bundle path requires an accessible current directory"
      local_bundle="$caller_directory/$local_bundle"
      ;;
  esac
fi

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" 2>/dev/null && pwd -P) ||
  die "the setup script directory is inaccessible; create a new project directory under your home folder"

if [ -z "$project_directory" ]; then
  project_directory=$script_directory
  if [ "$assume_yes" = false ] && [ -t 1 ] && [ -r /dev/tty ]; then
    printf '\nContain Yourself will keep its compose.yaml, .env, config/, and data/\n'
    printf 'together in one project directory.\n\n'
    printf 'Use this directory?\n  %s\n' "$script_directory"
    printf 'Continue here? [Y/n] '
    if IFS= read -r answer </dev/tty; then
      case "$answer" in
        n|N|no|NO|No)
          printf 'New directory [%s/contain-yourself]: ' "$script_directory"
          IFS= read -r chosen </dev/tty || chosen=
          if [ -z "$chosen" ]; then
            project_directory="$script_directory/contain-yourself"
          else
            case "$chosen" in
              "~") project_directory=${HOME:?HOME is not set} ;;
              "~/"*) project_directory="${HOME:?HOME is not set}/${chosen#\~/}" ;;
              /*) project_directory=$chosen ;;
              *) project_directory="$script_directory/$chosen" ;;
            esac
          fi
          ;;
      esac
    fi
  fi
fi
mkdir -p "$project_directory"
project_directory=$(CDPATH= cd -- "$project_directory" 2>/dev/null && pwd -P) ||
  die "cannot access project directory: $project_directory"

if [ "$project_directory" != "$script_directory" ]; then
  [ -f "$script_directory/compose.yaml" ] ||
    die "compose.yaml is missing beside setup.sh in $script_directory"
  cp "$script_directory/compose.yaml" "$project_directory/compose.yaml"
  if [ -f "$script_directory/setup.sh" ]; then
    cp "$script_directory/setup.sh" "$project_directory/setup.sh"
    chmod 755 "$project_directory/setup.sh"
  fi
  if [ -f "$script_directory/update.sh" ]; then
    cp "$script_directory/update.sh" "$project_directory/update.sh"
    chmod 755 "$project_directory/update.sh"
  fi
fi
cd "$project_directory"

[ -f compose.yaml ] ||
  die "compose.yaml is missing; download it into $project_directory before running setup"

printf '%s\n' "$repository" | grep -Eq '^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$' ||
  die "repository must be OWNER/REPO"
printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' ||
  die "version must be vMAJOR.MINOR.PATCH"

command -v tar >/dev/null 2>&1 || die "tar is required"
if [ -z "$local_bundle" ]; then
  command -v curl >/dev/null 2>&1 || die "curl is required"
fi

temporary=$(mktemp -d "${TMPDIR:-/tmp}/contain-yourself-setup.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
archive="$temporary/workstation-manager-bundle.tar.gz"

if [ -n "$local_bundle" ]; then
  [ -f "$local_bundle" ] || die "bundle does not exist: $local_bundle"
  cp "$local_bundle" "$archive"
else
  release_url=${WM_RELEASE_BASE_URL:-"https://github.com/$repository/releases/download/$version"}
  printf 'Downloading configuration for Contain Yourself %s...\n' "$version"
  curl -fsSL --retry 3 "$release_url/workstation-manager-bundle.tar.gz" -o "$archive"
  curl -fsSL --retry 3 "$release_url/workstation-manager-bundle.tar.gz.sha256" \
    -o "$temporary/checksum"
  expected=$(awk '$2 == "workstation-manager-bundle.tar.gz" {print $1}' "$temporary/checksum")
  [ -n "$expected" ] || die "release checksum is malformed"
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$archive" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$archive" | awk '{print $1}')
  else
    die "sha256sum or shasum is required to verify the download"
  fi
  [ "$actual" = "$expected" ] || die "release checksum verification failed"
fi

tar -tzf "$archive" >"$temporary/members"
while IFS= read -r member; do
  case "$member" in
    workstation-manager|workstation-manager/*) ;;
    *) die "release contains a path outside workstation-manager/" ;;
  esac
  case "/$member/" in
    */../*) die "release contains an unsafe path" ;;
  esac
done <"$temporary/members"
tar -xzf "$archive" -C "$temporary"
payload="$temporary/workstation-manager"
[ -f "$payload/VERSION" ] || die "release has no VERSION"
[ -d "$payload/config/apps" ] || die "release has no app configuration"
[ -d "$payload/config/templates" ] || die "release has no template configuration"
bundle_version=$(tr -d '\r\n' <"$payload/VERSION")
[ "$bundle_version" = "$version" ] ||
  die "configuration bundle is $bundle_version but setup expects $version"

mkdir -p data/apps data/app-store data/vpn-profiles data/backups data/worker data/workstation-logs
if [ ! -d config/apps ] || [ ! -d config/templates ] || [ "$refresh_config" = true ]; then
  mkdir -p config
  cp -R "$payload/config/." config/
  printf 'Installed core configuration in %s/config\n' "$project_directory"
else
  printf 'Keeping existing config/ (use --refresh-config to update core files).\n'
fi

env_file="$project_directory/.env"
touch "$env_file"
chmod 600 "$env_file"

quote_env() {
  escaped=$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')
  printf '"%s"' "$escaped"
}

set_env() {
  key=$1
  value=$2
  line="$key=$(quote_env "$value")"
  next=$(mktemp "$project_directory/.env.XXXXXX")
  awk -v key="$key" -v line="$line" '
    BEGIN { found = 0 }
    index($0, key "=") == 1 { if (!found) print line; found = 1; next }
    { print }
    END { if (!found) print line }
  ' "$env_file" >"$next"
  chmod 600 "$next"
  mv "$next" "$env_file"
}

ensure_env() {
  if ! grep -q "^$1=" "$env_file"; then
    set_env "$1" "$2"
  fi
}

repository_lower=$(printf '%s' "$repository" | tr '[:upper:]' '[:lower:]')
docker_endpoint=${DOCKER_HOST:-}
if [ -z "$docker_endpoint" ] && command -v docker >/dev/null 2>&1; then
  docker_endpoint=$(docker context inspect --format '{{.Endpoints.docker.Host}}' 2>/dev/null || true)
fi
case "$docker_endpoint" in
  unix://*) docker_socket=${docker_endpoint#unix://} ;;
  "") docker_socket=/var/run/docker.sock ;;
  *) die "the worker requires a local Unix Docker socket, not $docker_endpoint" ;;
esac

set_env WM_GITHUB_REPOSITORY "$repository"
set_env WM_IMAGE_REPOSITORY "ghcr.io/$repository_lower"
set_env WM_VERSION "$version"
ensure_env DATA_DIRECTORY "./data"
ensure_env CONFIG_DIRECTORY "./config"
ensure_env CONTROLLER_BIND "127.0.0.1"
ensure_env PUBLIC_BASE_DOMAIN ""
ensure_env SECURE_COOKIES "false"
ensure_env SESSION_LIFETIME "24h"
ensure_env DOCKER_SOCKET "$docker_socket"
ensure_env WORKER_DATA_SOURCE "./data/worker"
ensure_env APP_STORE_INDEX_URL "https://raw.githubusercontent.com/$repository/main/app_store/index.json"
ensure_env CONTROLLER_UID "$(id -u)"
ensure_env CONTROLLER_GID "$(id -g)"

if ! grep -q '^WORKER_TOKEN=' "$env_file"; then
  if command -v openssl >/dev/null 2>&1; then
    worker_token=$(openssl rand -hex 32)
  else
    worker_token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
  fi
  set_env WORKER_TOKEN "$worker_token"
fi

printf '\nPrepared Contain Yourself %s in %s\n' "$version" "$project_directory"
printf 'No containers were started. Start them when ready with:\n\n'
printf '  docker compose up -d\n\n'
printf 'Then open http://127.0.0.1:7080\n'
