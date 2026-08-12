#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/release.sh dev|stable [patch|minor|major]

Full-product release: the App Server (built in CI) and the macOS DMG (built
on this Mac) for the same channel, in one command.

  dev     dispatch the rolling App Server dev prerelease, publish the macOS
          dev DMG while CI builds, then wait for the CI run to finish.
  stable  dispatch the App Server vX.Y.Z release, wait for CI to publish it,
          then attach the macOS DMG to that release. The bump argument
          (default: patch) picks the version increment.
USAGE
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

CHANNEL="${1:-}"
BUMP="${2:-patch}"
case "$CHANNEL" in
  dev|stable) ;;
  -h|--help)
    usage
    exit 0
    ;;
  *)
    usage >&2
    die "expected channel dev or stable"
    ;;
esac
case "$BUMP" in
  patch|minor|major) ;;
  *)
    usage >&2
    die "expected bump patch, minor, or major"
    ;;
esac

command -v gh >/dev/null 2>&1 || die "Missing required tool: gh"

WORKFLOW="app-server-release.yml"

latest_run_id() {
  gh run list --workflow "$WORKFLOW" --limit 1 --json databaseId -q '.[0].databaseId' 2>/dev/null || true
}

# Capture the newest run before dispatching so the dispatched run is
# recognized by its id changing — `gh workflow run` returns no run id.
prev_run="$(latest_run_id)"

if [[ "$CHANNEL" == "stable" ]]; then
  mise run app-server:release:stable "$BUMP"
else
  mise run app-server:release:dev
fi

run_id="$prev_run"
for _ in $(seq 1 30); do
  run_id="$(latest_run_id)"
  if [[ -n "$run_id" && "$run_id" != "$prev_run" ]]; then
    break
  fi
  sleep 2
done
if [[ -z "$run_id" || "$run_id" == "$prev_run" ]]; then
  die "the dispatched App Server run never appeared; check gh run list --workflow $WORKFLOW"
fi

if [[ "$CHANNEL" == "stable" ]]; then
  # The DMG attaches to the vX.Y.Z release, so CI must publish it first.
  echo "waiting for the App Server release run to publish the new version..."
  gh run watch "$run_id" --exit-status
  mise run macos:release:stable
else
  # CI builds the server while this Mac builds the DMG.
  mise run macos:release:dev
  echo "macOS dev DMG published; waiting for the App Server run to finish..."
  gh run watch "$run_id" --exit-status
fi

echo "full $CHANNEL release complete"
