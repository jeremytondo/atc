#!/usr/bin/env bash
set -euo pipefail

TMP_PARENT="${ATC_TEST_TMP_PARENT:-/tmp}"
mkdir -p "$TMP_PARENT"

TMP_DIR="$(mktemp -d "$TMP_PARENT/atc.XXXXXX")"
cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

# macOS has a short Unix socket path limit, and its default TMPDIR path is long.
# Keep test sockets under a tiny per-run directory while preserving isolation.
export TMPDIR="$TMP_DIR"

go test ./... "$@"
