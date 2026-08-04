#!/bin/sh
set -eu

repository=${WM_GITHUB_REPOSITORY:-'@GITHUB_REPOSITORY@'}
project_directory=
apply_update=false

die() {
  printf 'contain-yourself update: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage: ./update.sh [--apply] [--directory PATH]

Downloads the latest Compose, setup, and update files, refreshes shipped
configuration, and preserves local data and secrets. By default it does not
pull images or restart containers.

Options:
  --apply           Pull images and recreate changed controller services
  --directory PATH  Existing Compose project (default: script directory)
  -h, --help        Show this help
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --apply)
      apply_update=true
      shift
      ;;
    --directory)
      [ "$#" -ge 2 ] || die "--directory requires a value"
      project_directory=$2
      shift 2
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

command -v curl >/dev/null 2>&1 || die "curl is required"
if [ -z "$project_directory" ]; then
  project_directory=$(CDPATH= cd -- "$(dirname -- "$0")" 2>/dev/null && pwd -P) ||
    die "cannot access the update script directory"
fi
project_directory=$(CDPATH= cd -- "$project_directory" 2>/dev/null && pwd -P) ||
  die "cannot access project directory: $project_directory"
[ -f "$project_directory/compose.yaml" ] || die "compose.yaml is missing from $project_directory"
[ -f "$project_directory/.env" ] || die ".env is missing; run setup.sh first"

temporary=$(mktemp -d "$project_directory/.contain-yourself-update.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
release_url=${WM_RELEASE_BASE_URL:-"https://github.com/$repository/releases/latest/download"}

printf 'Downloading the latest Contain Yourself release files...\n'
for asset in compose.yaml setup.sh update.sh; do
  curl -fsSL --retry 3 "$release_url/$asset" -o "$temporary/$asset"
done
chmod 755 "$temporary/setup.sh" "$temporary/update.sh"
mv "$temporary/compose.yaml" "$project_directory/compose.yaml"
mv "$temporary/setup.sh" "$project_directory/setup.sh"
mv "$temporary/update.sh" "$project_directory/update.sh"

"$project_directory/setup.sh" --directory "$project_directory" --refresh-config >/dev/null
version=$(awk -F= '$1 == "WM_VERSION" { gsub(/^"|"$/, "", $2); print $2; exit }' \
  "$project_directory/.env")
printf 'Prepared Contain Yourself %s. Data, custom templates, and secrets were preserved.\n' "$version"

if [ "$apply_update" = true ]; then
  command -v docker >/dev/null 2>&1 || die "Docker is required with --apply"
  cd "$project_directory"
  docker compose pull
  docker compose up -d
  printf 'Update applied. Open http://127.0.0.1:7080\n'
else
  printf 'No containers were changed. Apply the update when ready:\n\n'
  printf '  cd "%s"\n' "$project_directory"
  printf '  docker compose pull\n'
  printf '  docker compose up -d\n'
fi
