#!/usr/bin/env bash
# Performs the short, serialized GitHub mutation after all artifacts exist.
set -euo pipefail

usage() {
  echo "usage: scripts/publish-release.sh --kind stable|dev|candidate --channel dev|stable --tag TAG --version VERSION --commit SHA --built-at TIME --assets DIR" >&2
}

KIND=""
CHANNEL=""
TAG=""
VERSION=""
COMMIT=""
BUILT_AT=""
ASSET_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --kind) KIND="${2:-}"; shift 2 ;;
    --channel) CHANNEL="${2:-}"; shift 2 ;;
    --tag) TAG="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --commit) COMMIT="${2:-}"; shift 2 ;;
    --built-at) BUILT_AT="${2:-}"; shift 2 ;;
    --assets) ASSET_DIR="${2:-}"; shift 2 ;;
    *) usage; exit 2 ;;
  esac
done

case "$KIND" in
  stable|dev|candidate) ;;
  *) usage; exit 2 ;;
esac
case "$CHANNEL" in
  dev|stable) ;;
  *) usage; exit 2 ;;
esac
[[ -n "$TAG" && -n "$VERSION" && -n "$BUILT_AT" && "$COMMIT" =~ ^[0-9a-f]{40}$ && -d "$ASSET_DIR" ]] || { usage; exit 2; }
[[ "$KIND" == "stable" && "$CHANNEL" == "stable" ]] || [[ "$KIND" != "stable" && "$CHANNEL" == "dev" ]] || { usage; exit 2; }

expected=(
  atc-darwin-arm64.tar.gz
  atc-darwin-x64.tar.gz
  atc-linux-arm64.tar.gz
  atc-linux-x64.tar.gz
  atc-macos-arm64.dmg
  checksums.txt
  manifest.json
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
if [[ "$KIND" == "dev" ]]; then
  REMOTE_MAIN="$(git ls-remote origin refs/heads/main | awk '{ print $1 }')"
  [[ -n "$REMOTE_MAIN" ]] || { echo "could not resolve origin/main" >&2; exit 1; }
  if [[ "$REMOTE_MAIN" != "$COMMIT" ]]; then
    echo "skipping obsolete dev build $COMMIT; main is $REMOTE_MAIN"
    exit 0
  fi
fi

if [[ "$KIND" == "candidate" ]]; then
  [[ "$TAG" =~ ^dev-pr-([1-9][0-9]*)$ ]] || { echo "candidate releases must use a dev-pr-N tag" >&2; exit 1; }
  PR_NUMBER="${BASH_REMATCH[1]}"
  PR_STATE="$(gh pr view "$PR_NUMBER" --json state --jq .state 2>/dev/null || true)"
  if [[ "$PR_STATE" != "OPEN" ]]; then
    echo "skipping candidate build for closed PR #$PR_NUMBER"
    exit 0
  fi
  REMOTE_PR_HEAD="$(gh pr view "$PR_NUMBER" --json headRefOid --jq .headRefOid 2>/dev/null || true)"
  [[ -n "$REMOTE_PR_HEAD" ]] || { echo "could not resolve PR #$PR_NUMBER head" >&2; exit 1; }
  if [[ "$REMOTE_PR_HEAD" != "$COMMIT" ]]; then
    echo "skipping obsolete candidate build $COMMIT; PR #$PR_NUMBER is $REMOTE_PR_HEAD"
    exit 0
  fi
fi

git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"

if [[ "$KIND" == "stable" ]]; then
  [[ "$TAG" == "v$VERSION" ]] || { echo "stable tag/version mismatch: $TAG / $VERSION" >&2; exit 1; }
  git tag -a "$TAG" "$COMMIT" -m "Release $TAG"
  git push origin "$TAG"
  gh release create "$TAG" "${assets[@]}" --title "$TAG" --generate-notes --verify-tag
  exit 0
fi

if [[ "$KIND" == "dev" ]]; then
  [[ "$TAG" == "dev" ]] || { echo "rolling dev releases must use the dev tag" >&2; exit 1; }
  TITLE="ATC Dev — ${BUILT_AT/T/ }"
  TITLE="${TITLE%Z} UTC"
  NOTES="$(printf 'Version: `%s`  \nCommit: `%s`\n\nInstall or update the App Server:\n\n    curl -fsSL https://raw.githubusercontent.com/%s/main/install.sh | sh -s -- --channel dev\n\nInstalled builds update with `atc upgrade`. The attached manifest records the exact component set.' "$VERSION" "$COMMIT" "$GITHUB_REPOSITORY")"
else
  TITLE="ATC Dev Candidate — PR #${PR_NUMBER} — ${BUILT_AT/T/ }"
  TITLE="${TITLE%Z} UTC"
  NOTES="$(printf 'Version: `%s`  \nCommit: `%s`\n\nInstall the candidate App Server:\n\n    curl -fsSL https://raw.githubusercontent.com/%s/main/install.sh | sh -s -- --version %s\n\nThis candidate does not update automatically; rebuild or reinstall the PR candidate to test a newer revision. The attached manifest records the exact component set.' "$VERSION" "$COMMIT" "$GITHUB_REPOSITORY" "$TAG")"
fi

git tag -f "$TAG" "$COMMIT"
git push -f origin "refs/tags/$TAG"
if gh release view "$TAG" >/dev/null 2>&1; then
  gh release upload "$TAG" "${assets[@]}" --clobber
  gh release edit "$TAG" --title "$TITLE" --notes "$NOTES" --prerelease
else
  gh release create "$TAG" "${assets[@]}" --title "$TITLE" --notes "$NOTES" --prerelease --verify-tag
fi
