#!/usr/bin/env bash
# Hashes every source input that can affect one distributable component. The
# release manifest uses this content identity to reuse a previously signed or
# compiled artifact without relying on path-filter guesses.
set -euo pipefail

usage() {
  echo "usage: scripts/release-fingerprint.sh app-server|macos [COMMIT]" >&2
}

COMPONENT="${1:-}"
COMMIT="${2:-HEAD}"
case "$COMPONENT" in
  app-server)
    PATHS=(
      app-server
      mise.toml
      .github/workflows/product-release.yml
      scripts/package-app-server.sh
      scripts/release-fingerprint.sh
      scripts/release-plan.sh
    )
    ;;
  macos)
    PATHS=(
      macos
      packages
      app-server/openapi.json
      mise.toml
      .github/workflows/product-release.yml
      scripts/ExportOptions.DeveloperID.plist
      scripts/prepare-xcode-openapi.sh
      scripts/release-fingerprint.sh
      scripts/release-macos.sh
      scripts/release-plan.sh
    )
    ;;
  *) usage; exit 2 ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="${ATC_RELEASE_REPO_ROOT:-$(cd "$SCRIPT_DIR/.." && pwd -P)}"
git -C "$REPO_ROOT" rev-parse --verify "${COMMIT}^{commit}" >/dev/null
for path in "${PATHS[@]}"; do
  git -C "$REPO_ROOT" cat-file -e "$COMMIT:$path"
done

{
  printf 'component %s\n' "$COMPONENT"
  git -C "$REPO_ROOT" ls-tree -r "$COMMIT" -- "${PATHS[@]}"
} | shasum -a 256 | awk '{ print $1 }'
