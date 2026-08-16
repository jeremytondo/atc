#!/usr/bin/env bash
# Computes the one immutable identity consumed by every product build job.
set -euo pipefail

usage() {
  echo "usage: scripts/release-plan.sh stable [patch|minor|major] | dev [dev|dev-pr-N]" >&2
}

CHANNEL="${1:-}"
case "$CHANNEL" in
  dev|stable) ;;
  *) usage; exit 2 ;;
esac

if [[ "$CHANNEL" == "stable" ]]; then
  BUMP="${2:-patch}"
  case "$BUMP" in
    patch|minor|major) ;;
    *) usage; exit 2 ;;
  esac
  [[ $# -le 2 ]] || { usage; exit 2; }
else
  TAG="${2:-dev}"
  [[ "$TAG" == "dev" || "$TAG" =~ ^dev-pr-[1-9][0-9]*$ ]] || { usage; exit 2; }
  [[ $# -le 2 ]] || { usage; exit 2; }
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="${ATC_RELEASE_REPO_ROOT:-$(cd "$SCRIPT_DIR/.." && pwd -P)}"
COMMIT="$(git -C "$REPO_ROOT" rev-parse HEAD)"
SHORT_COMMIT="${COMMIT:0:8}"
BUILD_NUMBER="$(git -C "$REPO_ROOT" rev-list --count HEAD)"
BUILT_AT="${ATC_RELEASE_BUILT_AT:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

if [[ "$CHANNEL" == "stable" ]]; then
  NEXT_TAG="$(cd "$REPO_ROOT" && "$SCRIPT_DIR/next-version.sh" "$BUMP")"
  MARKETING_VERSION="${NEXT_TAG#v}"
  TAG="$NEXT_TAG"
  VERSION="$MARKETING_VERSION"
  KIND="stable"
else
  YEAR="${BUILT_AT:0:4}"
  MONTH="$((10#${BUILT_AT:5:2}))"
  DAY="$((10#${BUILT_AT:8:2}))"
  TIME="${BUILT_AT:11:2}${BUILT_AT:14:2}${BUILT_AT:17:2}"
  MARKETING_VERSION="${YEAR}.${MONTH}.${DAY}"
  VERSION="${MARKETING_VERSION}-dev.t${TIME}+${SHORT_COMMIT}"
  if [[ "$TAG" == "dev" ]]; then
    KIND="dev"
  else
    KIND="candidate"
  fi
fi

printf 'kind=%s\n' "$KIND"
printf 'channel=%s\n' "$CHANNEL"
printf 'tag=%s\n' "$TAG"
printf 'version=%s\n' "$VERSION"
printf 'marketing_version=%s\n' "$MARKETING_VERSION"
printf 'build_number=%s\n' "$BUILD_NUMBER"
printf 'commit=%s\n' "$COMMIT"
printf 'built_at=%s\n' "$BUILT_AT"
