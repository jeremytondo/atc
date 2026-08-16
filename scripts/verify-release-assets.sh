#!/usr/bin/env bash
# Verifies named release assets against one checksum file. Reuse may validate a
# component subset; publication uses --exact to require one canonical entry for
# every asset in the final product set and no extras.
set -euo pipefail

usage() {
  echo "usage: scripts/verify-release-assets.sh [--exact] CHECKSUMS ASSET_DIR ASSET [...]" >&2
}

EXACT=false
if [[ "${1:-}" == "--exact" ]]; then
  EXACT=true
  shift
fi

CHECKSUMS="${1:-}"
ASSET_DIR="${2:-}"
[[ -f "$CHECKSUMS" && -d "$ASSET_DIR" && $# -gt 2 ]] || { usage; exit 2; }
shift 2
EXPECTED_COUNT=$#

for name in "$@"; do
  [[ -n "$name" && "$name" != */* ]] || { echo "asset name must be a basename: $name" >&2; exit 2; }
  path="$ASSET_DIR/$name"
  [[ -f "$path" ]] || { echo "missing release asset: $path" >&2; exit 1; }
  expected="$(awk -v file="$name" '{ candidate=$NF; sub(/^\*/, "", candidate); if (candidate == file) print $1 }' "$CHECKSUMS")"
  [[ "$expected" =~ ^[0-9a-f]{64}$ ]] || { echo "$(basename "$CHECKSUMS") does not include exactly one valid checksum for $name" >&2; exit 1; }
  actual="$(shasum -a 256 "$path" | awk '{ print $1 }')"
  [[ "$actual" == "$expected" ]] || { echo "checksum mismatch for $name" >&2; exit 1; }
done

if [[ "$EXACT" == "true" ]]; then
  ENTRY_COUNT="$(awk 'NF { count++ } END { print count + 0 }' "$CHECKSUMS")"
  [[ "$ENTRY_COUNT" -eq "$EXPECTED_COUNT" ]] || {
    echo "$(basename "$CHECKSUMS") contains unexpected checksum entries" >&2
    exit 1
  }
fi
