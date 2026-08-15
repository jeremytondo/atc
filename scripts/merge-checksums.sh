#!/usr/bin/env bash
set -euo pipefail

DIR="${1:-}"
[[ -n "$DIR" && -d "$DIR" ]] || { echo "usage: scripts/merge-checksums.sh DIR" >&2; exit 2; }

shopt -s nullglob
parts=("$DIR"/checksums-*.txt)
[[ ${#parts[@]} -gt 0 ]] || { echo "no checksum fragments in $DIR" >&2; exit 1; }
cat "${parts[@]}" | sort -k2 > "$DIR/checksums.txt"
rm "${parts[@]}"
