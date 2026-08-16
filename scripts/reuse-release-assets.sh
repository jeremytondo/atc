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

TMP="$(mktemp -d "${TMPDIR:-/tmp}/atc-release-reuse.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
gh release download "$TAG" --pattern checksums.txt --dir "$TMP"
mkdir -p "$OUT"

assets=()
for name in "$@"; do
  [[ "$name" != */* ]] || { echo "asset name must be a basename: $name" >&2; exit 2; }
  expected="$(awk -v file="$name" '{ candidate=$NF; sub(/^\*/, "", candidate); if (candidate == file) print $1 }' "$TMP/checksums.txt")"
  [[ "$expected" =~ ^[0-9a-f]{64}$ ]] || { echo "checksums.txt does not include $name" >&2; exit 1; }
  gh release download "$TAG" --pattern "$name" --dir "$TMP"
  actual="$(shasum -a 256 "$TMP/$name" | awk '{ print $1 }')"
  [[ "$actual" == "$expected" ]] || { echo "checksum mismatch for $name from $TAG" >&2; exit 1; }
  cp "$TMP/$name" "$OUT/$name"
  assets+=("$name")
done

(
  cd "$OUT"
  shasum -a 256 "${assets[@]}" > "$CHECKSUMS"
)
