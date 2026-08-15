#!/usr/bin/env bash
# Computes the one immutable identity consumed by every product build job.
set -euo pipefail

usage() {
  echo "usage: scripts/release-plan.sh dev|stable [patch|minor|major]" >&2
}

CHANNEL="${1:-}"
BUMP="${2:-patch}"
case "$CHANNEL" in
  dev|stable) ;;
  *) usage; exit 2 ;;
esac
case "$BUMP" in
  patch|minor|major) ;;
  *) usage; exit 2 ;;
esac
if [[ "$CHANNEL" == "dev" && $# -gt 1 ]]; then
  usage
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="${ATC_RELEASE_REPO_ROOT:-$(cd "$SCRIPT_DIR/.." && pwd -P)}"
COMMIT="$(git -C "$REPO_ROOT" rev-parse HEAD)"
SHORT_COMMIT="${COMMIT:0:12}"
BUILD_NUMBER="$(git -C "$REPO_ROOT" rev-list --count HEAD)"
BUILT_AT="${ATC_RELEASE_BUILT_AT:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

NEXT_TAG="$(cd "$REPO_ROOT" && "$SCRIPT_DIR/next-version.sh" "$BUMP")"
MARKETING_VERSION="${NEXT_TAG#v}"
if [[ "$CHANNEL" == "stable" ]]; then
  TAG="$NEXT_TAG"
  VERSION="$MARKETING_VERSION"
else
  TAG="dev"
  VERSION="${MARKETING_VERSION}-dev.${BUILD_NUMBER}+${SHORT_COMMIT}"
fi

printf 'channel=%s\n' "$CHANNEL"
printf 'tag=%s\n' "$TAG"
printf 'version=%s\n' "$VERSION"
printf 'marketing_version=%s\n' "$MARKETING_VERSION"
printf 'build_number=%s\n' "$BUILD_NUMBER"
printf 'commit=%s\n' "$COMMIT"
printf 'built_at=%s\n' "$BUILT_AT"
