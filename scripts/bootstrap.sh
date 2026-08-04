#!/bin/sh
set -eu

repository=${WM_GITHUB_REPOSITORY:-'@GITHUB_REPOSITORY@'}
version=${WM_VERSION:-'@RELEASE_VERSION@'}
project_directory=

if [ -t 1 ] && [ "${TERM:-dumb}" != dumb ]; then
  esc=$(printf '\033')
  bold="${esc}[1m"
  cyan="${esc}[36m"
  green="${esc}[32m"
  yellow="${esc}[33m"
  reset="${esc}[0m"
else
  bold=
  cyan=
  green=
  yellow=
  reset=
fi

die() {
  printf '\n%sError:%s %s\n' "$yellow" "$reset" "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage: bootstrap.sh [--directory PATH]

Downloads and prepares a Contain Yourself Compose project. Docker is not
started. When piped from curl, questions are read directly from the terminal.

Options:
  --directory PATH  Prepare this directory without prompting
  -h, --help        Show this help
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
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

if base_directory=$(pwd -P 2>/dev/null); then
  :
elif [ -n "${HOME:-}" ] && base_directory=$(CDPATH= cd -- "$HOME" 2>/dev/null && pwd -P); then
  printf '%sNote:%s The current directory is inaccessible, so your home directory will be used.\n' \
    "$yellow" "$reset"
else
  die "the current directory is inaccessible and HOME cannot be used"
fi
cd "$base_directory" 2>/dev/null ||
  die "cannot enter base directory: $base_directory"

printf '\n%s%s' "$cyan" "$bold"
printf '+----------------------------------------------------------+\n'
printf '|                    CONTAIN YOURSELF                      |\n'
printf '|          Private container workstations, your way       |\n'
printf '+----------------------------------------------------------+\n'
printf '%s\n' "$reset"
printf 'This installs a Compose project only. Docker will not be started.\n\n'

if [ -z "$project_directory" ]; then
  if [ -t 1 ] && [ -r /dev/tty ]; then
    printf 'Where should the project live?\n\n'
    printf '  %s1)%s Create %s/contain-yourself %s(recommended)%s\n' \
      "$green" "$reset" "$base_directory" "$bold" "$reset"
    printf '  2) Use %s\n' "$base_directory"
    printf '  3) Choose another directory\n\n'
    printf 'Selection [1]: '
    IFS= read -r selection </dev/tty || selection=
    case "$selection" in
      ""|1)
        project_directory="$base_directory/contain-yourself"
        ;;
      2)
        project_directory=$base_directory
        ;;
      3)
        printf 'Directory path: '
        IFS= read -r project_directory </dev/tty ||
          die "could not read a directory"
        [ -n "$project_directory" ] || die "directory cannot be empty"
        ;;
      *)
        die "please choose 1, 2, or 3"
        ;;
    esac
  else
    project_directory="$base_directory/contain-yourself"
    printf 'No interactive terminal detected; using %s\n' "$project_directory"
  fi
fi

case "$project_directory" in
  "~") project_directory=${HOME:?HOME is not set} ;;
  "~/"*) project_directory="${HOME:?HOME is not set}/${project_directory#\~/}" ;;
  /*) ;;
  *) project_directory="$base_directory/$project_directory" ;;
esac

mkdir -p "$project_directory"
project_directory=$(CDPATH= cd -- "$project_directory" 2>/dev/null && pwd -P) ||
  die "cannot access project directory: $project_directory"

if [ -n "$(ls -A "$project_directory" 2>/dev/null)" ] &&
   [ ! -f "$project_directory/compose.yaml" ] &&
   [ ! -f "$project_directory/.env" ]; then
  if [ -t 1 ] && [ -r /dev/tty ]; then
    printf '\n%sWarning:%s %s already contains other files.\n' \
      "$yellow" "$reset" "$project_directory"
    printf 'Add the Contain Yourself project files here? [y/N] '
    IFS= read -r answer </dev/tty || answer=
    case "$answer" in
      y|Y|yes|YES|Yes) ;;
      *) die "setup cancelled; choose an empty directory instead" ;;
    esac
  else
    die "$project_directory is not empty; choose a dedicated directory"
  fi
fi

printf '\n%s[1/3]%s Downloading release files for %s...\n' "$cyan" "$reset" "$version"
release_url=${WM_RELEASE_BASE_URL:-"https://github.com/$repository/releases/download/$version"}
temporary=$(mktemp -d "$project_directory/.contain-yourself-download.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
curl -fsSL --retry 3 "$release_url/compose.yaml" -o "$temporary/compose.yaml"
curl -fsSL --retry 3 "$release_url/setup.sh" -o "$temporary/setup.sh"
curl -fsSL --retry 3 "$release_url/update.sh" -o "$temporary/update.sh"
chmod 755 "$temporary/setup.sh" "$temporary/update.sh"
mv "$temporary/compose.yaml" "$project_directory/compose.yaml"
mv "$temporary/setup.sh" "$project_directory/setup.sh"
mv "$temporary/update.sh" "$project_directory/update.sh"

printf '%s[2/3]%s Verifying and preparing config and data...\n' "$cyan" "$reset"
"$project_directory/setup.sh" --directory "$project_directory" >/dev/null

printf '%s[3/3]%s Ready.\n\n' "$green" "$reset"
printf '%s%sContain Yourself %s is prepared.%s\n' "$bold" "$green" "$version" "$reset"
printf 'Project directory:\n  %s\n\n' "$project_directory"
printf 'Nothing has been started. When you are ready:\n\n'
printf '  cd "%s"\n' "$project_directory"
printf '  docker compose up -d\n\n'
printf 'Then open http://127.0.0.1:7080\n'
printf 'Docs: https://github.com/%s/blob/main/docs/installation.md\n' "$repository"
