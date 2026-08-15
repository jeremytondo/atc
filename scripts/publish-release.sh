#!/usr/bin/env bash
# Performs the short, serialized GitHub mutation after all artifacts exist.
set -euo pipefail

usage() {
  echo "usage: scripts/publish-release.sh --channel dev|stable --tag TAG --version VERSION --commit SHA --assets DIR" >&2
}

CHANNEL=""
TAG=""
VERSION=""
COMMIT=""
ASSET_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --channel) CHANNEL="${2:-}"; shift 2 ;;
    --tag) TAG="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --commit) COMMIT="${2:-}"; shift 2 ;;
    --assets) ASSET_DIR="${2:-}"; shift 2 ;;
    *) usage; exit 2 ;;
  esac
done

case "$CHANNEL" in
  dev|stable) ;;
  *) usage; exit 2 ;;
esac
[[ -n "$TAG" && -n "$VERSION" && "$COMMIT" =~ ^[0-9a-f]{40}$ && -d "$ASSET_DIR" ]] || { usage; exit 2; }

expected=(
  atc-darwin-arm64.tar.gz
  atc-darwin-x64.tar.gz
  atc-linux-arm64.tar.gz
  atc-linux-x64.tar.gz
  atc-macos-arm64.dmg
  checksums.txt
)
assets=()
for name in "${expected[@]}"; do
  path="$ASSET_DIR/$name"
  [[ -f "$path" ]] || { echo "missing release asset: $path" >&2; exit 1; }
  assets+=("$path")
done

# A stable dispatch releases the immutable main snapshot selected when the
# workflow started. Later main pushes do not invalidate artifacts from that
# snapshot. The rolling dev release, however, should never move backwards to an
# obsolete build, so check its freshness immediately before updating dev.
if [[ "$CHANNEL" == "dev" ]]; then
  REMOTE_MAIN="$(git ls-remote origin refs/heads/main | awk '{ print $1 }')"
  [[ -n "$REMOTE_MAIN" ]] || { echo "could not resolve origin/main" >&2; exit 1; }
  if [[ "$REMOTE_MAIN" != "$COMMIT" ]]; then
    echo "skipping obsolete dev build $COMMIT; main is $REMOTE_MAIN"
    exit 0
  fi
fi

git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"

if [[ "$CHANNEL" == "stable" ]]; then
  [[ "$TAG" == "v$VERSION" ]] || { echo "stable tag/version mismatch: $TAG / $VERSION" >&2; exit 1; }
  git tag -a "$TAG" "$COMMIT" -m "Release $TAG"
  git push origin "$TAG"
  gh release create "$TAG" "${assets[@]}" --title "$TAG" --generate-notes --verify-tag
  exit 0
fi

[[ "$TAG" == "dev" ]] || { echo "dev releases must use the rolling dev tag" >&2; exit 1; }
git tag -f dev "$COMMIT"
git push -f origin dev
NOTES="$(printf 'Rolling whole-product build of commit %s.\n\nInstall or update the App Server:\n\n    curl -fsSL https://raw.githubusercontent.com/%s/main/install.sh | sh -s -- --channel dev\n\nInstalled builds update with `atc upgrade`.' "$COMMIT" "$GITHUB_REPOSITORY")"
if gh release view dev >/dev/null 2>&1; then
  gh release upload dev "${assets[@]}" --clobber
  gh release edit dev --title "ATC dev ($VERSION)" --notes "$NOTES" --prerelease
else
  gh release create dev "${assets[@]}" --title "ATC dev ($VERSION)" --notes "$NOTES" --prerelease --verify-tag
fi
