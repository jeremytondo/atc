#!/usr/bin/env bash
# Dispatches stable whole-product releases; publication always happens in CI.
set -euo pipefail

usage() {
  echo "usage: mise run release patch|minor|major" >&2
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

[[ $# -eq 1 ]] || { usage; exit 2; }
BUMP="$1"
case "$BUMP" in
  patch|minor|major) ;;
  *) usage; exit 2 ;;
esac

command -v gh >/dev/null 2>&1 || die "Missing required tool: gh"
gh auth status -h github.com >/dev/null 2>&1 || die "gh is not authenticated for github.com"

WORKFLOW="product-release.yml"
latest_run_id() {
  gh run list \
    --workflow "$WORKFLOW" \
    --event workflow_dispatch \
    --branch main \
    --limit 1 \
    --json databaseId \
    -q '.[0].databaseId' 2>/dev/null || true
}

PREVIOUS_RUN="$(latest_run_id)"
gh workflow run "$WORKFLOW" --ref main -f release_type="$BUMP"
printf 'dispatched stable %s release from main\n' "$BUMP"

RUN_ID="$PREVIOUS_RUN"
for _ in $(seq 1 30); do
  RUN_ID="$(latest_run_id)"
  if [[ -n "$RUN_ID" && "$RUN_ID" != "$PREVIOUS_RUN" ]]; then
    break
  fi
  sleep 2
done
if [[ -z "$RUN_ID" || "$RUN_ID" == "$PREVIOUS_RUN" ]]; then
  die "the dispatched run never appeared; check gh run list --workflow $WORKFLOW"
fi

gh run watch "$RUN_ID" --exit-status
echo "stable whole-product release complete"
