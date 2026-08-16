#!/usr/bin/env bash
# Selects reusable component identities from newest-to-oldest manifests.
# Missing, malformed, or stale manifests are cache misses, never failures.
set -euo pipefail

usage() {
  echo "usage: scripts/select-release-components.sh APP_SERVER_FINGERPRINT MACOS_FINGERPRINT [MANIFEST ...]" >&2
}

APP_SERVER_FINGERPRINT="${1:-}"
MACOS_FINGERPRINT="${2:-}"
[[ "$APP_SERVER_FINGERPRINT" =~ ^[0-9a-f]{64}$ && "$MACOS_FINGERPRINT" =~ ^[0-9a-f]{64}$ ]] || { usage; exit 2; }
shift 2

select_component() {
  local component="$1"
  local fingerprint="$2"
  local manifest tag
  for manifest in "$@"; do
    [[ -f "$manifest" ]] || continue
    jq -e --arg component "$component" --arg fingerprint "$fingerprint" '
      .schemaVersion == 1 and
      .release.tag != null and
      .components[$component].fingerprint == $fingerprint and
      (.components[$component].version | type == "string" and length > 0) and
      (.components[$component].commit | type == "string" and test("^[0-9a-f]{40}$")) and
      (.components[$component].builtAt | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$"))
    ' "$manifest" >/dev/null 2>&1 || continue
    tag="$(jq -r '.release.tag // empty' "$manifest")"
    [[ "$tag" == "dev" || "$tag" =~ ^dev-pr-[1-9][0-9]*$ ]] || continue
    printf '%s\n' "$manifest"
    return
  done
}

emit_component() {
  local output_name="$1"
  local component="$2"
  local fingerprint="$3"
  shift 3
  local manifest=""
  manifest="$(select_component "$component" "$fingerprint" "$@" || true)"
  if [[ -z "$manifest" ]]; then
    printf 'reuse_%s=false\n' "$output_name"
    return
  fi
  printf 'reuse_%s=true\n' "$output_name"
  printf '%s_source_tag=%s\n' "$output_name" "$(jq -r '.release.tag' "$manifest")"
  printf '%s_version=%s\n' "$output_name" "$(jq -r ".components.${component}.version" "$manifest")"
  printf '%s_commit=%s\n' "$output_name" "$(jq -r ".components.${component}.commit" "$manifest")"
  printf '%s_built_at=%s\n' "$output_name" "$(jq -r ".components.${component}.builtAt" "$manifest")"
}

emit_component app_server appServer "$APP_SERVER_FINGERPRINT" "$@"
emit_component macos macos "$MACOS_FINGERPRINT" "$@"
