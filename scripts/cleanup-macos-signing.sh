#!/usr/bin/env bash
# Removes the release identity and API key before later workflow actions run.
set -euo pipefail

CREDENTIAL_DIR="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/atc-release-credentials"
KEYCHAIN_PATH="$CREDENTIAL_DIR/release.keychain-db"
ORIGINAL_KEYCHAINS_PATH="$CREDENTIAL_DIR/original-keychains"
ORIGINAL_DEFAULT_KEYCHAIN_PATH="$CREDENTIAL_DIR/original-default-keychain"

if command -v security >/dev/null 2>&1; then
  original_keychains=()
  if [[ -f "$ORIGINAL_KEYCHAINS_PATH" ]]; then
    while IFS= read -r keychain; do
      [[ -n "$keychain" ]] && original_keychains+=("$keychain")
    done < "$ORIGINAL_KEYCHAINS_PATH"
  fi
  if [[ ${#original_keychains[@]} -gt 0 ]]; then
    security list-keychains -d user -s "${original_keychains[@]}" >/dev/null 2>&1 || true
  fi

  if [[ -s "$ORIGINAL_DEFAULT_KEYCHAIN_PATH" ]]; then
    original_default_keychain="$(< "$ORIGINAL_DEFAULT_KEYCHAIN_PATH")"
    security default-keychain -d user -s "$original_default_keychain" >/dev/null 2>&1 || true
  fi

  security delete-keychain "$KEYCHAIN_PATH" >/dev/null 2>&1 || true
fi

rm -rf "$CREDENTIAL_DIR"
