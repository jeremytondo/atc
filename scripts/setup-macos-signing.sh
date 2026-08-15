#!/usr/bin/env bash
# Installs release credentials into an ephemeral runner keychain.
set -euo pipefail
umask 077

required=(
  ATC_DEVELOPER_ID_CERTIFICATE_BASE64
  ATC_DEVELOPER_ID_CERTIFICATE_PASSWORD
  ATC_APP_STORE_CONNECT_KEY_BASE64
  ATC_APP_STORE_CONNECT_KEY_ID
  ATC_APP_STORE_CONNECT_ISSUER_ID
)
for name in "${required[@]}"; do
  [[ -n "${!name:-}" ]] || { echo "missing release credential: $name" >&2; exit 1; }
done

CREDENTIAL_DIR="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/atc-release-credentials"
KEYCHAIN_PATH="$CREDENTIAL_DIR/release.keychain-db"
CERTIFICATE_PATH="$CREDENTIAL_DIR/developer-id.p12"
APP_STORE_CONNECT_KEY_PATH="$CREDENTIAL_DIR/AuthKey.p8"
KEYCHAIN_PASSWORD="$(uuidgen)"

if [[ -e "$CREDENTIAL_DIR" ]]; then
  echo "release credential directory already exists: $CREDENTIAL_DIR" >&2
  exit 1
fi
mkdir -m 700 "$CREDENTIAL_DIR"
printf '%s' "$ATC_DEVELOPER_ID_CERTIFICATE_BASE64" | base64 --decode > "$CERTIFICATE_PATH"
printf '%s' "$ATC_APP_STORE_CONNECT_KEY_BASE64" | base64 --decode > "$APP_STORE_CONNECT_KEY_PATH"
chmod 600 "$CERTIFICATE_PATH" "$APP_STORE_CONNECT_KEY_PATH"

security create-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN_PATH"
security set-keychain-settings -lut 3600 "$KEYCHAIN_PATH"
security unlock-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN_PATH"
security import "$CERTIFICATE_PATH" \
  -k "$KEYCHAIN_PATH" \
  -P "$ATC_DEVELOPER_ID_CERTIFICATE_PASSWORD" \
  -T /usr/bin/codesign \
  -T /usr/bin/security
security set-key-partition-list \
  -S apple-tool:,apple:,codesign: \
  -s \
  -k "$KEYCHAIN_PASSWORD" \
  "$KEYCHAIN_PATH"
security list-keychains -d user -s "$KEYCHAIN_PATH"
security default-keychain -d user -s "$KEYCHAIN_PATH"

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  printf 'app_store_connect_key_path=%s\n' "$APP_STORE_CONNECT_KEY_PATH" >> "$GITHUB_OUTPUT"
fi
