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
REQUEST_ID="$(printf '%s:%s:%s:%s' "$(date -u +%s)" "$$" "$RANDOM" "$RANDOM" | git hash-object --stdin)"
RUN_TITLE="Product Release [$REQUEST_ID]"

matching_run_id() {
  gh run list \
    --workflow "$WORKFLOW" \
    --event workflow_dispatch \
    --branch main \
    --limit 100 \
    --json databaseId,displayTitle \
    -q ".[] | select(.displayTitle == \"$RUN_TITLE\") | .databaseId" \
    2>/dev/null | head -n 1 || true
}

gh workflow run "$WORKFLOW" --ref main -f release_type="$BUMP" -f request_id="$REQUEST_ID"
printf 'dispatched stable %s release from main\n' "$BUMP"

RUN_ID=""
for _ in $(seq 1 30); do
  RUN_ID="$(matching_run_id)"
  if [[ -n "$RUN_ID" ]]; then
    break
  fi
  sleep 2
done
if [[ -z "$RUN_ID" ]]; then
  die "the dispatched run never appeared; check gh run list --workflow $WORKFLOW"
fi

gh run watch "$RUN_ID" --exit-status
echo "stable whole-product release complete"
