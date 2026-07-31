#!/bin/sh
set -eu

default_repository='@GITHUB_REPOSITORY@'
repository=${WM_GITHUB_REPOSITORY:-}
install_dir=${WM_INSTALL_DIR:-"${XDG_DATA_HOME:-$HOME/.local/share}/workstation-manager"}
requested_version=latest
local_bundle=
start=true

die() {
  printf 'workstation-manager installer: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage: install.sh [options]

Options:
  --repository OWNER/REPO  GitHub repository containing releases
  --version VERSION        Release tag to install (default: latest)
  --install-dir PATH       Installation directory
  --bundle PATH            Install a local release bundle (for development)
  --no-start               Install files without starting Docker services
  -h, --help               Show this help
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --repository)
      [ "$#" -ge 2 ] || die "--repository requires a value"
      repository=$2
      shift 2
      ;;
    --version)
      [ "$#" -ge 2 ] || die "--version requires a value"
      requested_version=$2
      shift 2
      ;;
    --install-dir)
      [ "$#" -ge 2 ] || die "--install-dir requires a value"
      install_dir=$2
      shift 2
      ;;
    --bundle)
      [ "$#" -ge 2 ] || die "--bundle requires a value"
      local_bundle=$2
      shift 2
      ;;
    --no-start)
      start=false
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

if [ -z "$repository" ]; then
  case "$default_repository" in
    @*) ;;
    *) repository=$default_repository ;;
  esac
fi
printf '%s\n' "$repository" | grep -Eq '^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$' ||
  die "pass --repository OWNER/REPO (the release workflow fills this automatically)"
if [ "$requested_version" != latest ]; then
  printf '%s\n' "$requested_version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' ||
    die "version must be latest or vMAJOR.MINOR.PATCH"
fi

command -v tar >/dev/null 2>&1 || die "tar is required"
command -v docker >/dev/null 2>&1 || die "Docker is required"
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is required"
if [ "$start" = true ]; then
  docker info >/dev/null 2>&1 || die "Docker is not running or is inaccessible"
fi
if [ -z "$local_bundle" ]; then
  command -v curl >/dev/null 2>&1 || die "curl is required"
fi

mkdir -p "$install_dir"
install_dir=$(CDPATH= cd -- "$install_dir" && pwd)

temporary=$(mktemp -d "${TMPDIR:-/tmp}/workstation-manager-install.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
archive="$temporary/workstation-manager-bundle.tar.gz"

if [ -n "$local_bundle" ]; then
  [ -f "$local_bundle" ] || die "bundle does not exist: $local_bundle"
  cp "$local_bundle" "$archive"
else
  if [ "$requested_version" = latest ]; then
    release_url="https://github.com/$repository/releases/latest/download"
  else
    release_url="https://github.com/$repository/releases/download/$requested_version"
  fi
  printf 'Downloading Workstation Manager from %s...\n' "$repository"
  curl -fL --retry 3 "$release_url/workstation-manager-bundle.tar.gz" -o "$archive"
  curl -fL --retry 3 "$release_url/workstation-manager-bundle.tar.gz.sha256" \
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
[ -f "$payload/compose.yaml" ] || die "release has no Compose file"
[ -x "$payload/manage" ] || die "release has no management command"
[ -d "$payload/config/apps" ] || die "release has no app configuration"
[ -d "$payload/config/templates" ] || die "release has no template configuration"

release_version=$(tr -d '\r\n' <"$payload/VERSION")
printf '%s\n' "$release_version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' ||
  die "release contains an invalid version"
if [ "$requested_version" != latest ] && [ "$requested_version" != "$release_version" ]; then
  die "downloaded $release_version but requested $requested_version"
fi

mkdir -p "$install_dir/releases" "$install_dir/data"
release_dir="$install_dir/releases/$release_version"
if [ ! -d "$release_dir" ]; then
  incoming="$install_dir/releases/.incoming-$release_version-$$"
  mkdir "$incoming"
  cp -R "$payload/." "$incoming/"
  mv "$incoming" "$release_dir"
fi
cp "$payload/manage" "$install_dir/manage"
chmod 755 "$install_dir/manage"

if [ -e "$install_dir/current" ] && [ ! -L "$install_dir/current" ]; then
  die "$install_dir/current exists and is not a managed symlink"
fi
new_link="$install_dir/.current-$$"
ln -s "releases/$release_version" "$new_link"
rm -f "$install_dir/current"
mv -f "$new_link" "$install_dir/current"
if [ ! -e "$install_dir/config" ] && [ ! -L "$install_dir/config" ]; then
  ln -s "current/config" "$install_dir/config"
fi

env_file="$install_dir/.env"
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
  next=$(mktemp "$install_dir/.env.XXXXXX")
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
if [ -z "$docker_endpoint" ]; then
  docker_endpoint=$(docker context inspect --format '{{.Endpoints.docker.Host}}' 2>/dev/null || true)
fi
case "$docker_endpoint" in
  unix://*) docker_socket=${docker_endpoint#unix://} ;;
  "") docker_socket=/var/run/docker.sock ;;
  *) die "the worker requires a local Unix Docker socket, not $docker_endpoint" ;;
esac

set_env WM_GITHUB_REPOSITORY "$repository"
set_env WM_IMAGE_REPOSITORY "ghcr.io/$repository_lower"
set_env WM_VERSION "$release_version"
ensure_env DATA_DIRECTORY "$install_dir/data"
ensure_env CONFIG_DIRECTORY "$install_dir/config"
ensure_env CONTROLLER_BIND "127.0.0.1"
ensure_env PUBLIC_BASE_DOMAIN ""
ensure_env SECURE_COOKIES "false"
ensure_env SESSION_LIFETIME "24h"
ensure_env DOCKER_SOCKET "$docker_socket"

host_uid=$(id -u)
host_gid=$(id -g)
if [ "$host_uid" = 0 ]; then
  controller_uid=10001
  controller_gid=10001
  chown -R "$controller_uid:$controller_gid" "$install_dir/data"
else
  controller_uid=$host_uid
  controller_gid=$host_gid
fi
ensure_env CONTROLLER_UID "$controller_uid"
ensure_env CONTROLLER_GID "$controller_gid"

if ! grep -q '^WORKER_TOKEN=' "$env_file"; then
  if command -v openssl >/dev/null 2>&1; then
    worker_token=$(openssl rand -hex 32)
  else
    worker_token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
  fi
  set_env WORKER_TOKEN "$worker_token"
fi

bin_dir=${WM_BIN_DIR:-"$HOME/.local/bin"}
mkdir -p "$bin_dir"
for command_name in wm workstation-manager; do
  command_path="$bin_dir/$command_name"
  if [ ! -e "$command_path" ] || [ -L "$command_path" ]; then
    ln -sfn "$install_dir/manage" "$command_path"
  else
    printf 'Not replacing existing command: %s\n' "$command_path" >&2
  fi
done

printf 'Installed Workstation Manager %s in %s\n' "$release_version" "$install_dir"
if [ "$start" = true ]; then
  "$install_dir/manage" start
else
  printf 'Start it with: %s/manage start\n' "$install_dir"
fi
case ":${PATH:-}:" in
  *":$bin_dir:"*) ;;
  *) printf 'Add %s to PATH to use the wm command.\n' "$bin_dir" ;;
esac
