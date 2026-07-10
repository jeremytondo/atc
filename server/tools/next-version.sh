#!/bin/sh
set -eu

release_type="${1:-}"

case "$release_type" in
  patch|minor|major) ;;
  *)
    echo "usage: tools/next-version.sh patch|minor|major" >&2
    exit 1
    ;;
esac

latest="$(
  git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=v:refname |
    awk '/^v[0-9]+\.[0-9]+\.[0-9]+$/ { latest=$0 } END { print latest }'
)"

if [ -z "$latest" ]; then
  latest="v0.0.0"
fi

version="${latest#v}"
major="${version%%.*}"
rest="${version#*.}"
minor="${rest%%.*}"
patch="${rest#*.}"

case "$release_type" in
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

next="v${major}.${minor}.${patch}"
if git rev-parse -q --verify "refs/tags/${next}" >/dev/null; then
  echo "tag already exists: ${next}" >&2
  exit 1
fi

printf '%s\n' "$next"

