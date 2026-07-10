#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/release-dev.sh [--upload]

Builds, exports, packages, notarizes, staples, and verifies a Developer ID
DMG for AtelierCode Dev.app. With --upload, also creates a GitHub prerelease
and uploads the notarized DMG.

Environment overrides:
  AC_TEAM_ID          Apple Developer Team ID (default: 337D6CNU4E)
  AC_NOTARY_PROFILE  notarytool keychain profile (default: ateliercode-notary)
  AC_ARTIFACT_ROOT   artifact root (default: .build/release-dev)
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

UPLOAD=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --upload)
      UPLOAD=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      die "Unknown argument: $1"
      ;;
  esac
  shift
done

AC_TEAM_ID="${AC_TEAM_ID:-337D6CNU4E}"
AC_NOTARY_PROFILE="${AC_NOTARY_PROFILE:-ateliercode-notary}"
AC_ARTIFACT_ROOT="${AC_ARTIFACT_ROOT:-.build/release-dev}"

APP_NAME="AtelierCode Dev"
BUNDLE_ID="ElevenIdeas.AtelierCode.dev"
PROJECT_REL="macos/AtelierCode.xcodeproj"
SCHEME="AtelierCode"
CONFIGURATION="Release"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd -P)"
PROJECT_PATH="$REPO_ROOT/$PROJECT_REL"
EXPORT_OPTIONS_PLIST="$SCRIPT_DIR/ExportOptions.DeveloperID.plist"

TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
TAG="dev-$TIMESTAMP"
TITLE="AtelierCode Dev $TIMESTAMP"
RUN_DIR="$REPO_ROOT/$AC_ARTIFACT_ROOT/$TIMESTAMP"
ARCHIVE_PATH="$RUN_DIR/AtelierCodeDev.xcarchive"
EXPORT_PATH="$RUN_DIR/export"
DERIVED_DATA_PATH="$RUN_DIR/DerivedData"
SOURCE_PACKAGES_PATH="$RUN_DIR/SourcePackages"
DMG_ROOT="$RUN_DIR/dmg-root"
DMG_PATH="$RUN_DIR/AtelierCode-Dev-$TIMESTAMP.dmg"
APP_PATH="$EXPORT_PATH/$APP_NAME.app"

DEVELOPER_ID_IDENTITY=""

find_developer_id_identity() {
  local identities
  local line
  identities="$(security find-identity -v -p codesigning 2>&1 || true)"
  while IFS= read -r line; do
    case "$line" in
      *"Developer ID Application:"*"($AC_TEAM_ID)"*)
        DEVELOPER_ID_IDENTITY="${line#*\"}"
        DEVELOPER_ID_IDENTITY="${DEVELOPER_ID_IDENTITY%%\"*}"
        break
        ;;
    esac
  done <<< "$identities"

  if [[ -z "$DEVELOPER_ID_IDENTITY" ]]; then
    printf '%s\n' "$identities" >&2
    die "No valid Developer ID Application identity found for Team ID $AC_TEAM_ID. Install the certificate and private key, then rerun."
  fi
}

validate_notary_profile() {
  if ! xcrun notarytool history --keychain-profile "$AC_NOTARY_PROFILE" --no-progress >/dev/null 2>&1; then
    die "notarytool profile '$AC_NOTARY_PROFILE' is unavailable or invalid. Store credentials with xcrun notarytool store-credentials, then rerun."
  fi
}

validate_github_auth() {
  if [[ "$UPLOAD" -eq 1 ]]; then
    require_tool gh
    gh auth status -h github.com >/dev/null 2>&1 || die "gh is not authenticated for github.com. Run gh auth login, then rerun with --upload."
  fi
}

validate_exported_app() {
  [[ -d "$APP_PATH" ]] || die "Expected exported app not found: $APP_PATH"

  local actual_bundle_id
  actual_bundle_id="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$APP_PATH/Contents/Info.plist")"
  [[ "$actual_bundle_id" == "$BUNDLE_ID" ]] || die "Expected bundle ID $BUNDLE_ID, got $actual_bundle_id"
}

require_tool xcodebuild
require_tool xcrun
require_tool hdiutil
require_tool security
require_tool codesign
require_tool spctl
require_tool ditto
[[ -x /usr/libexec/PlistBuddy ]] || die "Missing required tool: /usr/libexec/PlistBuddy"
[[ -d "$PROJECT_PATH" ]] || die "Missing Xcode project: $PROJECT_PATH"
[[ -f "$EXPORT_OPTIONS_PLIST" ]] || die "Missing export options: $EXPORT_OPTIONS_PLIST"

log "Validating signing, notarization, and upload prerequisites"
find_developer_id_identity
validate_notary_profile
validate_github_auth

mkdir -p "$RUN_DIR" "$EXPORT_PATH" "$DERIVED_DATA_PATH" "$SOURCE_PACKAGES_PATH" "$DMG_ROOT"

XCODE_OVERRIDES=(
  "ARCHS=arm64"
  "ONLY_ACTIVE_ARCH=NO"
  "PRODUCT_NAME=$APP_NAME"
  "PRODUCT_BUNDLE_IDENTIFIER=$BUNDLE_ID"
  "DEVELOPMENT_TEAM=$AC_TEAM_ID"
  "CODE_SIGN_STYLE=Automatic"
)

log "Archiving $APP_NAME.app"
xcodebuild archive \
  -project "$PROJECT_PATH" \
  -scheme "$SCHEME" \
  -configuration "$CONFIGURATION" \
  -destination "generic/platform=macOS" \
  -archivePath "$ARCHIVE_PATH" \
  -derivedDataPath "$DERIVED_DATA_PATH" \
  -clonedSourcePackagesDirPath "$SOURCE_PACKAGES_PATH" \
  -skipPackagePluginValidation \
  -skipMacroValidation \
  "${XCODE_OVERRIDES[@]}"

log "Exporting Developer ID app"
xcodebuild -exportArchive \
  -archivePath "$ARCHIVE_PATH" \
  -exportPath "$EXPORT_PATH" \
  -exportOptionsPlist "$EXPORT_OPTIONS_PLIST" \
  -allowProvisioningUpdates

validate_exported_app

log "Verifying exported app signature"
codesign --verify --deep --strict --verbose=2 "$APP_PATH"

log "Creating DMG"
ditto "$APP_PATH" "$DMG_ROOT/$APP_NAME.app"
ln -s /Applications "$DMG_ROOT/Applications"
hdiutil create \
  -volname "$APP_NAME" \
  -srcfolder "$DMG_ROOT" \
  -ov \
  -format UDZO \
  "$DMG_PATH"

log "Signing DMG"
codesign --force --sign "$DEVELOPER_ID_IDENTITY" "$DMG_PATH"
codesign --verify --verbose=2 "$DMG_PATH"

log "Submitting DMG for notarization"
xcrun notarytool submit "$DMG_PATH" \
  --keychain-profile "$AC_NOTARY_PROFILE" \
  --wait

log "Stapling notarization ticket"
xcrun stapler staple "$DMG_PATH"
xcrun stapler validate "$DMG_PATH"

log "Assessing DMG with Gatekeeper"
spctl --assess --type open --context context:primary-signature --verbose=4 "$DMG_PATH"

if [[ "$UPLOAD" -eq 1 ]]; then
  log "Creating GitHub prerelease $TAG"
  gh release create "$TAG" "$DMG_PATH" \
    --title "$TITLE" \
    --notes "Developer ID notarized Apple Silicon dev build for $APP_NAME." \
    --prerelease
fi

log "Release artifact ready"
printf 'Tag: %s\n' "$TAG"
printf 'DMG: %s\n' "$DMG_PATH"
