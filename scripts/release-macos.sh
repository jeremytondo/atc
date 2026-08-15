#!/usr/bin/env bash
# Builds the signed, notarized macOS artifact for a precomputed product identity.
# This script never publishes; GitHub mutation stays in the workflow's final job.
set -euo pipefail

usage() {
  cat <<'USAGE'
usage: scripts/release-macos.sh \
  --channel dev|stable \
  --version VERSION \
  --marketing-version X.Y.Z \
  --build-number NUMBER \
  --commit SHA \
  --built-at ISO-8601 \
  --output PATH [--verbose]

Notarization uses either ATC_NOTARY_PROFILE or all of
ATC_NOTARY_KEY_PATH, ATC_NOTARY_KEY_ID, and ATC_NOTARY_ISSUER_ID.
USAGE
}

log() {
  printf '\n==> %s\n' "$*"
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_tool() {
  command -v "$1" >/dev/null 2>&1 || die "Missing required tool: $1"
}

CHANNEL=""
VERSION=""
MARKETING_VERSION=""
BUILD_NUMBER=""
COMMIT=""
BUILT_AT=""
OUTPUT_PATH=""
VERBOSE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --channel) CHANNEL="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --marketing-version) MARKETING_VERSION="${2:-}"; shift 2 ;;
    --build-number) BUILD_NUMBER="${2:-}"; shift 2 ;;
    --commit) COMMIT="${2:-}"; shift 2 ;;
    --built-at) BUILT_AT="${2:-}"; shift 2 ;;
    --output) OUTPUT_PATH="${2:-}"; shift 2 ;;
    --verbose) VERBOSE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "Unknown argument: $1" ;;
  esac
done

case "$CHANNEL" in
  dev|stable) ;;
  *) usage >&2; die "channel must be dev or stable" ;;
esac
[[ -n "$VERSION" ]] || die "--version is required"
[[ "$MARKETING_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "--marketing-version must be X.Y.Z"
[[ "$BUILD_NUMBER" =~ ^[1-9][0-9]*$ ]] || die "--build-number must be a positive integer"
[[ "$COMMIT" =~ ^[0-9a-f]{40}$ ]] || die "--commit must be a full Git commit SHA"
[[ -n "$BUILT_AT" ]] || die "--built-at is required"
[[ -n "$OUTPUT_PATH" ]] || die "--output is required"

ATC_TEAM_ID="${ATC_TEAM_ID:-337D6CNU4E}"
ATC_ARTIFACT_ROOT="${ATC_ARTIFACT_ROOT:-.build/release-macos}"

APP_NAME="atc"
SCHEME="atc"
CONFIGURATION="Release"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd -P)"
PROJECT_PATH="$REPO_ROOT/macos/atc.xcodeproj"
EXPORT_OPTIONS_PLIST="$SCRIPT_DIR/ExportOptions.DeveloperID.plist"
if [[ "$OUTPUT_PATH" != /* ]]; then
  OUTPUT_PATH="$REPO_ROOT/$OUTPUT_PATH"
fi

case "$CHANNEL" in
  stable) BUNDLE_ID="ElevenIdeas.atc" ;;
  dev) BUNDLE_ID="ElevenIdeas.atc.dev" ;;
esac

RUN_DIR="$REPO_ROOT/$ATC_ARTIFACT_ROOT/$BUILD_NUMBER-$CHANNEL"
ARCHIVE_PATH="$RUN_DIR/atc-$CHANNEL.xcarchive"
EXPORT_PATH="$RUN_DIR/export"
DERIVED_DATA_PATH="$RUN_DIR/DerivedData"
SOURCE_PACKAGES_PATH="$RUN_DIR/SourcePackages"
DMG_ROOT="$RUN_DIR/dmg-root"
DMG_PATH="$RUN_DIR/atc-macos-arm64.dmg"
APP_PATH="$EXPORT_PATH/$APP_NAME.app"
LOG_DIR="$RUN_DIR/logs"

DEVELOPER_ID_IDENTITY=""
NOTARY_ARGS=()
XCODE_AUTH_ARGS=(-allowProvisioningUpdates)

find_developer_id_identity() {
  local identities line
  identities="$(security find-identity -v -p codesigning 2>&1 || true)"
  while IFS= read -r line; do
    case "$line" in
      *"Developer ID Application:"*"($ATC_TEAM_ID)"*)
        DEVELOPER_ID_IDENTITY="${line#*\"}"
        DEVELOPER_ID_IDENTITY="${DEVELOPER_ID_IDENTITY%%\"*}"
        break
        ;;
    esac
  done <<< "$identities"

  if [[ -z "$DEVELOPER_ID_IDENTITY" ]]; then
    printf '%s\n' "$identities" >&2
    die "No valid Developer ID Application identity found for Team ID $ATC_TEAM_ID"
  fi
}

configure_notary_credentials() {
  if [[ -n "${ATC_NOTARY_PROFILE:-}" ]]; then
    NOTARY_ARGS=(--keychain-profile "$ATC_NOTARY_PROFILE")
    return
  fi
  if [[ -n "${ATC_NOTARY_KEY_PATH:-}" && -n "${ATC_NOTARY_KEY_ID:-}" && -n "${ATC_NOTARY_ISSUER_ID:-}" ]]; then
    [[ -f "$ATC_NOTARY_KEY_PATH" ]] || die "notary API key not found: $ATC_NOTARY_KEY_PATH"
    NOTARY_ARGS=(
      --key "$ATC_NOTARY_KEY_PATH"
      --key-id "$ATC_NOTARY_KEY_ID"
      --issuer "$ATC_NOTARY_ISSUER_ID"
    )
    # The ephemeral runner imports only the long-lived Developer ID identity.
    # Automatic archive signing can use the same App Store Connect API key to
    # obtain its short-lived development signing assets; export then selects
    # the locally imported Developer ID certificate.
    XCODE_AUTH_ARGS=(
      -authenticationKeyPath "$ATC_NOTARY_KEY_PATH"
      -authenticationKeyID "$ATC_NOTARY_KEY_ID"
      -authenticationKeyIssuerID "$ATC_NOTARY_ISSUER_ID"
      -allowProvisioningUpdates
    )
    return
  fi
  die "configure ATC_NOTARY_PROFILE or the three ATC_NOTARY_KEY_* credentials"
}

validate_notary_credentials() {
  local output
  if output="$(xcrun notarytool history "${NOTARY_ARGS[@]}" --no-progress 2>&1)"; then
    return
  fi
  printf '%s\n' "$output" >&2
  die "notarytool could not use the configured credentials"
}

plist_value() {
  /usr/libexec/PlistBuddy -c "Print :$1" "$APP_PATH/Contents/Info.plist"
}

validate_exported_app() {
  [[ -d "$APP_PATH" ]] || { printf 'Expected exported app not found: %s\n' "$APP_PATH" >&2; return 1; }
  assert_plist_value CFBundleIdentifier "$BUNDLE_ID"
  assert_plist_value CFBundleShortVersionString "$MARKETING_VERSION"
  assert_plist_value CFBundleVersion "$BUILD_NUMBER"
  assert_plist_value ATCBuildVersion "$VERSION"
  assert_plist_value ATCBuildCommit "$COMMIT"
  assert_plist_value ATCBuildBuiltAt "$BUILT_AT"
}

assert_plist_value() {
  local key="$1"
  local expected="$2"
  local actual
  actual="$(plist_value "$key")"
  if [[ "$actual" != "$expected" ]]; then
    printf 'Expected Info.plist %s=%s, got %s\n' "$key" "$expected" "$actual" >&2
    return 1
  fi
}

run_step() {
  local label="$1"
  local log_name="$2"
  local log_path="$LOG_DIR/$log_name.log"
  local status
  shift 2

  if [[ "$VERBOSE" -eq 1 ]]; then
    log "$label"
    if "$@" 2>&1 | tee "$log_path"; then
      return
    else
      status=$?
    fi
    printf 'error: %s failed (full log: %s)\n' "$label" "$log_path" >&2
    return "$status"
  fi

  printf '  %-58s' "$label"
  if "$@" >"$log_path" 2>&1; then
    printf '✓\n'
    return
  else
    status=$?
  fi
  printf '✗\n\n' >&2
  cat "$log_path" >&2
  printf 'error: %s failed (full log: %s)\n' "$label" "$log_path" >&2
  return "$status"
}

create_dmg() {
  ditto "$APP_PATH" "$DMG_ROOT/$APP_NAME.app" &&
    ln -s /Applications "$DMG_ROOT/Applications" &&
    hdiutil create -volname "$APP_NAME" -srcfolder "$DMG_ROOT" -ov -format UDZO "$DMG_PATH"
}

sign_dmg() {
  codesign --force --sign "$DEVELOPER_ID_IDENTITY" "$DMG_PATH" &&
    codesign --verify --verbose=2 "$DMG_PATH"
}

staple_dmg() {
  xcrun stapler staple "$DMG_PATH" && xcrun stapler validate "$DMG_PATH"
}

for tool in xcodebuild xcrun hdiutil security codesign spctl ditto; do
  require_tool "$tool"
done
[[ -x /usr/libexec/PlistBuddy ]] || die "Missing required tool: /usr/libexec/PlistBuddy"
[[ -d "$PROJECT_PATH" ]] || die "Missing Xcode project: $PROJECT_PATH"
[[ -f "$EXPORT_OPTIONS_PLIST" ]] || die "Missing export options: $EXPORT_OPTIONS_PLIST"

printf '\nmacOS release artifact (%s, %s)\n\n' "$CHANNEL" "$VERSION"
printf '  %-58s' "Validate signing and notarization prerequisites"
find_developer_id_identity
configure_notary_credentials
validate_notary_credentials
printf '✓\n'

mkdir -p "$RUN_DIR" "$EXPORT_PATH" "$DERIVED_DATA_PATH" "$SOURCE_PACKAGES_PATH" "$DMG_ROOT" "$LOG_DIR"

run_step "Generate App Server API sources" "01-generate-api" \
  "$SCRIPT_DIR/prepare-xcode-openapi.sh" "$DERIVED_DATA_PATH"

XCODE_OVERRIDES=(
  "ARCHS=arm64"
  "ONLY_ACTIVE_ARCH=NO"
  "PRODUCT_NAME=$APP_NAME"
  "PRODUCT_BUNDLE_IDENTIFIER=$BUNDLE_ID"
  "DEVELOPMENT_TEAM=$ATC_TEAM_ID"
  "CODE_SIGN_STYLE=Automatic"
  "MARKETING_VERSION=$MARKETING_VERSION"
  "CURRENT_PROJECT_VERSION=$BUILD_NUMBER"
  "ATC_BUILD_VERSION=$VERSION"
  "ATC_BUILD_COMMIT=$COMMIT"
  "ATC_BUILD_BUILT_AT=$BUILT_AT"
)

run_step "Archive $APP_NAME.app" "02-archive" xcodebuild archive \
  -project "$PROJECT_PATH" \
  -scheme "$SCHEME" \
  -configuration "$CONFIGURATION" \
  -destination "generic/platform=macOS" \
  -archivePath "$ARCHIVE_PATH" \
  -derivedDataPath "$DERIVED_DATA_PATH" \
  -clonedSourcePackagesDirPath "$SOURCE_PACKAGES_PATH" \
  -skipPackagePluginValidation \
  -skipMacroValidation \
  "${XCODE_AUTH_ARGS[@]}" \
  "${XCODE_OVERRIDES[@]}"

run_step "Export Developer ID app" "03-export" xcodebuild -exportArchive \
  -archivePath "$ARCHIVE_PATH" \
  -exportPath "$EXPORT_PATH" \
  -exportOptionsPlist "$EXPORT_OPTIONS_PLIST" \
  "${XCODE_AUTH_ARGS[@]}"

run_step "Validate exported app identity" "04-validate-app" validate_exported_app
run_step "Verify exported app signature" "05-verify-app" \
  codesign --verify --deep --strict --verbose=2 "$APP_PATH"
run_step "Create DMG" "06-create-dmg" create_dmg
run_step "Sign and verify DMG" "07-sign-dmg" sign_dmg
run_step "Submit DMG for notarization" "08-notarize" \
  xcrun notarytool submit "$DMG_PATH" "${NOTARY_ARGS[@]}" --wait
run_step "Staple notarization ticket" "09-staple" staple_dmg
run_step "Assess DMG with Gatekeeper" "10-gatekeeper" \
  spctl --assess --type open --context context:primary-signature --verbose=4 "$DMG_PATH"

mkdir -p "$(dirname "$OUTPUT_PATH")"
cp "$DMG_PATH" "$OUTPUT_PATH"

log "Release artifact ready"
printf 'Channel: %s\n' "$CHANNEL"
printf 'Version: %s\n' "$VERSION"
printf 'Commit: %s\n' "$COMMIT"
printf 'Built at: %s\n' "$BUILT_AT"
printf 'DMG: %s\n' "$OUTPUT_PATH"
printf 'Logs: %s\n' "$LOG_DIR"
