#!/bin/sh
set -eu

usage() {
  cat <<'EOF'
Usage: ./scripts/next-release.sh [major|minor|patch] [--yes] [--dry-run]

Calculates the next semantic version from the newest tag reachable from HEAD,
creates an annotated tag, and pushes it to origin. With no release type, the
script asks whether this is a major, minor, or patch release.
EOF
}

release_kind=
assume_yes=false
dry_run=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    major|minor|patch)
      [ -z "$release_kind" ] || {
        echo "choose only one release type" >&2
        exit 2
      }
      release_kind=$1
      ;;
    --yes|-y) assume_yes=true ;;
    --dry-run) dry_run=true ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

command -v git >/dev/null 2>&1 || {
  echo "git is required" >&2
  exit 1
}

[ -z "$(git status --porcelain)" ] || {
  echo "refusing to release from a dirty working tree" >&2
  exit 1
}

branch=$(git branch --show-current)
[ "$branch" = "main" ] || {
  echo "refusing to release from branch '$branch'; switch to main first" >&2
  exit 1
}

remote=${RELEASE_REMOTE:-origin}
echo "Refreshing $remote/main and release tags..."
git fetch "$remote" main --tags

head=$(git rev-parse HEAD)
remote_head=$(git rev-parse "$remote/main")
[ "$head" = "$remote_head" ] || {
  echo "HEAD does not match $remote/main; push or pull before releasing" >&2
  exit 1
}

current=$(
  git tag --merged HEAD --list 'v*' --sort=-v:refname |
    while IFS= read -r tag; do
      if printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
        printf '%s\n' "$tag"
        break
      fi
    done
)
current=${current:-v0.0.0}

if [ -z "$release_kind" ]; then
  echo "Current release: $current"
  echo "Choose the kind of release:"
  echo "  1) major  (x.0.0 — breaking change)"
  echo "  2) minor  (0.x.0 — new backwards-compatible feature)"
  echo "  3) patch  (0.0.x — bug fix)"
  printf "Selection [1-3]: "
  read -r selection
  case "$selection" in
    1|major) release_kind=major ;;
    2|minor) release_kind=minor ;;
    3|patch) release_kind=patch ;;
    *)
      echo "invalid release type" >&2
      exit 2
      ;;
  esac
fi

version=${current#v}
old_ifs=$IFS
IFS=.
set -- $version
IFS=$old_ifs
major=$1
minor=$2
patch=$3

case "$release_kind" in
  major)
    major=$((major + 1))
    minor=0
    patch=0
    ;;
  minor)
    minor=$((minor + 1))
    patch=0
    ;;
  patch)
    patch=$((patch + 1))
    ;;
esac
next="v$major.$minor.$patch"

if git rev-parse -q --verify "refs/tags/$next" >/dev/null; then
  echo "tag $next already exists" >&2
  exit 1
fi

echo "Current release: $current"
echo "Next $release_kind release: $next"
echo "Commit: $head"

if [ "$dry_run" = true ]; then
  echo "Dry run only; no tag was created."
  exit 0
fi

if [ "$assume_yes" != true ]; then
  printf "Create and push %s? [y/N]: " "$next"
  read -r confirmation
  case "$confirmation" in
    y|Y|yes|YES) ;;
    *)
      echo "Release cancelled."
      exit 0
      ;;
  esac
fi

git tag -a "$next" -m "Contain Yourself $next"
if ! git push "$remote" "$next"; then
  echo "push failed; local tag $next was kept so the failure can be inspected" >&2
  exit 1
fi

echo "Published $next. GitHub Actions will build the images and release assets."
