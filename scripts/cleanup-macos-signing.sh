#!/usr/bin/env bash
# Removes the release identity and API key before later workflow actions run.
set -euo pipefail

CREDENTIAL_DIR="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/atc-release-credentials"
KEYCHAIN_PATH="$CREDENTIAL_DIR/release.keychain-db"
CERTIFICATE_PATH="$CREDENTIAL_DIR/developer-id.p12"
APP_STORE_CONNECT_KEY_PATH="$CREDENTIAL_DIR/AuthKey.p8"

if command -v security >/dev/null 2>&1; then
  security delete-keychain "$KEYCHAIN_PATH" >/dev/null 2>&1 || true
fi

rm -f "$KEYCHAIN_PATH" "$CERTIFICATE_PATH" "$APP_STORE_CONNECT_KEY_PATH"
rmdir "$CREDENTIAL_DIR" 2>/dev/null || true
