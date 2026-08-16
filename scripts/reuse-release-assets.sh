#!/usr/bin/env bash
# Downloads immutable component assets from an existing rolling release and
# verifies them against that release's checksums before handing them to a new
# product assembly.
set -euo pipefail

usage() {
  echo "usage: scripts/reuse-release-assets.sh --tag TAG --out DIR --checksums NAME ASSET [...]" >&2
}

TAG=""
OUT=""
CHECKSUMS=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag) TAG="${2:-}"; shift 2 ;;
    --out) OUT="${2:-}"; shift 2 ;;
    --checksums) CHECKSUMS="${2:-}"; shift 2 ;;
    --) shift; break ;;
    -*) usage; exit 2 ;;
    *) break ;;
  esac
done

[[ "$TAG" == "dev" || "$TAG" =~ ^dev-pr-[1-9][0-9]*$ ]] || { usage; exit 2; }
[[ -n "$OUT" && -n "$CHECKSUMS" && "$CHECKSUMS" != */* && $# -gt 0 ]] || { usage; exit 2; }
command -v gh >/dev/null 2>&1 || { echo "required command not found: gh" >&2; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/atc-release-reuse.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
gh release download "$TAG" --pattern checksums.txt --dir "$TMP"
mkdir -p "$OUT"

assets=()
for name in "$@"; do
  [[ "$name" != */* ]] || { echo "asset name must be a basename: $name" >&2; exit 2; }
  gh release download "$TAG" --pattern "$name" --dir "$TMP"
  assets+=("$name")
done

"$SCRIPT_DIR/verify-release-assets.sh" "$TMP/checksums.txt" "$TMP" "${assets[@]}"
for name in "${assets[@]}"; do
  cp "$TMP/$name" "$OUT/$name"
done

(
  cd "$OUT"
  shasum -a 256 "${assets[@]}" > "$CHECKSUMS"
)
