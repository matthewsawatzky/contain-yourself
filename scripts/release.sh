#!/bin/sh
set -eu

version=${1:-}
output=${2:-dist}

if ! printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  printf 'usage: %s vMAJOR.MINOR.PATCH [output-directory]\n' "$0" >&2
  exit 2
fi

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "$output" in
  /*) ;;
  *) output="$root/$output" ;;
esac

stage=$(mktemp -d "${TMPDIR:-/tmp}/workstation-manager-release.XXXXXX")
trap 'rm -rf "$stage"' EXIT HUP INT TERM
bundle_root="$stage/workstation-manager"

mkdir -p "$bundle_root/config" "$output"
cp "$root/deploy/compose.yaml" "$bundle_root/compose.yaml"
cp "$root/scripts/manage.sh" "$bundle_root/manage"
cp -R "$root/core_apps" "$bundle_root/config/apps"
cp -R "$root/core_templates" "$bundle_root/config/templates"
printf '%s\n' "$version" >"$bundle_root/VERSION"
chmod 755 "$bundle_root/manage"

archive="$output/workstation-manager-bundle.tar.gz"
tar -C "$stage" -czf "$archive" workstation-manager

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$output" && sha256sum "$(basename "$archive")" >workstation-manager-bundle.tar.gz.sha256)
else
  (cd "$output" && shasum -a 256 "$(basename "$archive")" >workstation-manager-bundle.tar.gz.sha256)
fi

printf 'Created %s\n' "$archive"
