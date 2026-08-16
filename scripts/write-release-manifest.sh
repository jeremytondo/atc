#!/usr/bin/env bash
# Writes the bill of materials for one product release set. Component identity
# remains the identity embedded in the reused bytes, while release identity
# describes the newly assembled and compatibility-tested set.
set -euo pipefail

usage() {
  echo "usage: scripts/write-release-manifest.sh --kind KIND --tag TAG --version VERSION --commit SHA --built-at TIME --app-server-fingerprint SHA --app-server-version VERSION --app-server-commit SHA --app-server-built-at TIME --app-server-reused-from TAG --macos-fingerprint SHA --macos-version VERSION --macos-commit SHA --macos-built-at TIME --macos-reused-from TAG --output PATH" >&2
}

KIND="" TAG="" VERSION="" COMMIT="" BUILT_AT="" OUTPUT=""
APP_SERVER_FINGERPRINT="" APP_SERVER_VERSION="" APP_SERVER_COMMIT="" APP_SERVER_BUILT_AT="" APP_SERVER_REUSED_FROM=""
MACOS_FINGERPRINT="" MACOS_VERSION="" MACOS_COMMIT="" MACOS_BUILT_AT="" MACOS_REUSED_FROM=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --kind) KIND="${2:-}"; shift 2 ;;
    --tag) TAG="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --commit) COMMIT="${2:-}"; shift 2 ;;
    --built-at) BUILT_AT="${2:-}"; shift 2 ;;
    --app-server-fingerprint) APP_SERVER_FINGERPRINT="${2:-}"; shift 2 ;;
    --app-server-version) APP_SERVER_VERSION="${2:-}"; shift 2 ;;
    --app-server-commit) APP_SERVER_COMMIT="${2:-}"; shift 2 ;;
    --app-server-built-at) APP_SERVER_BUILT_AT="${2:-}"; shift 2 ;;
    --app-server-reused-from) APP_SERVER_REUSED_FROM="${2:-}"; shift 2 ;;
    --macos-fingerprint) MACOS_FINGERPRINT="${2:-}"; shift 2 ;;
    --macos-version) MACOS_VERSION="${2:-}"; shift 2 ;;
    --macos-commit) MACOS_COMMIT="${2:-}"; shift 2 ;;
    --macos-built-at) MACOS_BUILT_AT="${2:-}"; shift 2 ;;
    --macos-reused-from) MACOS_REUSED_FROM="${2:-}"; shift 2 ;;
    --output) OUTPUT="${2:-}"; shift 2 ;;
    *) usage; exit 2 ;;
  esac
done

case "$KIND" in stable|dev|candidate) ;; *) usage; exit 2 ;; esac
[[ -n "$TAG" && -n "$VERSION" && -n "$OUTPUT" ]] || { usage; exit 2; }
[[ -n "$APP_SERVER_VERSION" && -n "$MACOS_VERSION" ]] || { usage; exit 2; }
for value in "$BUILT_AT" "$APP_SERVER_BUILT_AT" "$MACOS_BUILT_AT"; do
  [[ "$value" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || {
    echo "invalid release timestamp: $value" >&2
    exit 1
  }
done
for value in "$COMMIT" "$APP_SERVER_COMMIT" "$MACOS_COMMIT"; do
  [[ "$value" =~ ^[0-9a-f]{40}$ ]] || { usage; exit 2; }
done
for value in "$APP_SERVER_FINGERPRINT" "$MACOS_FINGERPRINT"; do
  [[ "$value" =~ ^[0-9a-f]{64}$ ]] || { usage; exit 2; }
done

mkdir -p "$(dirname "$OUTPUT")"
jq -n \
  --arg kind "$KIND" --arg tag "$TAG" --arg version "$VERSION" \
  --arg commit "$COMMIT" --arg builtAt "$BUILT_AT" \
  --arg appFingerprint "$APP_SERVER_FINGERPRINT" --arg appVersion "$APP_SERVER_VERSION" \
  --arg appCommit "$APP_SERVER_COMMIT" --arg appBuiltAt "$APP_SERVER_BUILT_AT" \
  --arg appReusedFrom "$APP_SERVER_REUSED_FROM" \
  --arg macFingerprint "$MACOS_FINGERPRINT" --arg macVersion "$MACOS_VERSION" \
  --arg macCommit "$MACOS_COMMIT" --arg macBuiltAt "$MACOS_BUILT_AT" \
  --arg macReusedFrom "$MACOS_REUSED_FROM" \
  '{
    schemaVersion: 1,
    release: { kind: $kind, tag: $tag, version: $version, commit: $commit, builtAt: $builtAt },
    components: {
      appServer: {
        fingerprint: $appFingerprint, version: $appVersion, commit: $appCommit,
        builtAt: $appBuiltAt, reusedFrom: (if $appReusedFrom == "" then null else $appReusedFrom end)
      },
      macos: {
        fingerprint: $macFingerprint, version: $macVersion, commit: $macCommit,
        builtAt: $macBuiltAt, reusedFrom: (if $macReusedFrom == "" then null else $macReusedFrom end)
      }
    }
  }' > "$OUTPUT"
