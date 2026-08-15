#!/usr/bin/env bash
# Packages already-compiled App Server targets without rebuilding them.
set -euo pipefail

usage() {
  echo "usage: scripts/package-app-server.sh --dist DIR --out DIR --checksums NAME target [...]" >&2
}

DIST=""
OUT=""
CHECKSUMS=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dist) DIST="${2:-}"; shift 2 ;;
    --out) OUT="${2:-}"; shift 2 ;;
    --checksums) CHECKSUMS="${2:-}"; shift 2 ;;
    --) shift; break ;;
    -*) usage; exit 2 ;;
    *) break ;;
  esac
done

[[ -n "$DIST" && -n "$OUT" && -n "$CHECKSUMS" && $# -gt 0 ]] || { usage; exit 2; }
[[ "$CHECKSUMS" != */* ]] || { echo "checksum name must be a basename" >&2; exit 2; }
mkdir -p "$OUT"

archives=()
for target in "$@"; do
  case "$target" in
    darwin-arm64|darwin-x64|linux-arm64|linux-x64) ;;
    *) echo "unknown App Server target: $target" >&2; exit 2 ;;
  esac
  binary="$DIST/atc-$target"
  [[ -x "$binary" ]] || { echo "missing executable: $binary" >&2; exit 1; }
  staging="$(mktemp -d "${TMPDIR:-/tmp}/atc-package.XXXXXX")"
  cp "$binary" "$staging/atc"
  archive="$OUT/atc-$target.tar.gz"
  tar -czf "$archive" -C "$staging" atc
  rm -rf "$staging"
  archives+=("$archive")
done

(
  cd "$OUT"
  shasum -a 256 "${archives[@]##*/}" > "$CHECKSUMS"
)
