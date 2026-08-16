#!/usr/bin/env bash
# Dispatches one PR-scoped rolling dev candidate and watches it through
# publication. The remote PR head is the source of truth; local-only work is
# deliberately never pushed or inferred by release tooling.
set -euo pipefail

usage() {
  echo "usage: mise run dev-build [-- --pr NUMBER]" >&2
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

PR_NUMBER=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --pr)
      [[ $# -ge 2 && -n "$2" ]] || { usage; exit 2; }
      PR_NUMBER="$2"
      shift 2
      ;;
    *) usage; exit 2 ;;
  esac
done
[[ -z "$PR_NUMBER" || "$PR_NUMBER" =~ ^[1-9][0-9]*$ ]] || { usage; exit 2; }

command -v gh >/dev/null 2>&1 || die "Missing required tool: gh"
gh auth status -h github.com >/dev/null 2>&1 || die "gh is not authenticated for github.com"

if [[ -z "$PR_NUMBER" ]]; then
  PR_NUMBER="$(gh pr view --json number --jq .number 2>/dev/null)" ||
    die "the current checkout is not associated with an open PR; pass --pr NUMBER"
fi
IFS=$'\t' read -r HEAD_REF HEAD_SHA PR_STATE < <(
  gh pr view "$PR_NUMBER" --json headRefName,headRefOid,state \
    --jq '[.headRefName, .headRefOid, .state] | @tsv'
) || die "could not read PR #$PR_NUMBER"
[[ "$PR_STATE" == "OPEN" ]] || die "PR #$PR_NUMBER is not open"

WORKFLOW="product-release.yml"
REQUEST_ID="$(printf '%s:%s:%s:%s' "$(date -u +%s)" "$$" "$RANDOM" "$RANDOM" | git hash-object --stdin)"
RUN_TITLE="Product Release [candidate:$REQUEST_ID]"

matching_run_id() {
  gh run list \
    --workflow "$WORKFLOW" \
    --event workflow_dispatch \
    --branch "$HEAD_REF" \
    --limit 100 \
    --json databaseId,displayTitle \
    -q ".[] | select(.displayTitle == \"$RUN_TITLE\") | .databaseId" \
    2>/dev/null | head -n 1 || true
}

printf 'dispatching dev candidate for PR #%s at %s\n' "$PR_NUMBER" "${HEAD_SHA:0:8}"
# GitHub registers workflow_dispatch inputs from the default branch. This
# workflow must reach the default branch once before feature refs can use it.
gh workflow run "$WORKFLOW" \
  --ref "$HEAD_REF" \
  -f mode=candidate \
  -f pr_number="$PR_NUMBER" \
  -f request_id="$REQUEST_ID"

RUN_ID=""
for _ in $(seq 1 30); do
  RUN_ID="$(matching_run_id)"
  [[ -z "$RUN_ID" ]] || break
  sleep 2
done
[[ -n "$RUN_ID" ]] || die "the dispatched run never appeared; check gh run list --workflow $WORKFLOW"

gh run watch "$RUN_ID" --exit-status
IFS=$'\t' read -r CURRENT_HEAD PR_STATE < <(
  gh pr view "$PR_NUMBER" --json headRefOid,state --jq '[.headRefOid, .state] | @tsv'
) || die "could not read PR #$PR_NUMBER after the build"
[[ "$PR_STATE" == "OPEN" ]] ||
  die "PR #$PR_NUMBER closed during the build; the candidate was skipped"
[[ "$CURRENT_HEAD" == "$HEAD_SHA" ]] ||
  die "PR #$PR_NUMBER moved to ${CURRENT_HEAD:0:8} during the build; the candidate was skipped. Re-run mise run dev-build."
RELEASE_URL="$(gh release view "dev-pr-$PR_NUMBER" --json url --jq .url)" ||
  die "the run finished without publishing a candidate; check the run logs"
printf 'dev candidate ready: %s\n' "$RELEASE_URL"
