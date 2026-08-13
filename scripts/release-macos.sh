#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/release-macos.sh [--channel dev|stable] [--upload] [--verbose]

Builds, exports, packages, notarizes, staples, and verifies a Developer ID
DMG for atc.app.

  --channel dev     (default) dev bundle id (ElevenIdeas.atc.dev); --upload
                    creates a dev-<timestamp> GitHub prerelease with the DMG.
  --channel stable  stable bundle id, marketing version from the latest
                    vX.Y.Z tag; --upload attaches the DMG to that tag's
                    existing GitHub release (run the stable App Server
                    release first: mise run app-server:release:stable).
  --verbose         stream full command output instead of the concise release
                    checklist. Logs are always retained with the artifacts.

Environment overrides:
  ATC_TEAM_ID         Apple Developer Team ID (default: 337D6CNU4E)
  ATC_NOTARY_PROFILE  notarytool stored-credentials profile (default: ateliercode-notary)
  ATC_ARTIFACT_ROOT   artifact root (default: .build/release-macos)
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
CHANNEL="dev"
VERBOSE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --upload)
      UPLOAD=1
      ;;
    --channel)
      [[ $# -ge 2 ]] || die "missing value for --channel"
      CHANNEL="$2"
      shift
      ;;
    --verbose)
      VERBOSE=1
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

case "$CHANNEL" in
  dev|stable) ;;
  *) die "Unknown channel: $CHANNEL (expected dev or stable)" ;;
esac

ATC_TEAM_ID="${ATC_TEAM_ID:-337D6CNU4E}"
ATC_NOTARY_PROFILE="${ATC_NOTARY_PROFILE:-ateliercode-notary}"
ATC_ARTIFACT_ROOT="${ATC_ARTIFACT_ROOT:-.build/release-macos}"

APP_NAME="atc"
PROJECT_REL="macos/atc.xcodeproj"
SCHEME="atc"
CONFIGURATION="Release"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd -P)"
PROJECT_PATH="$REPO_ROOT/$PROJECT_REL"
EXPORT_OPTIONS_PLIST="$SCRIPT_DIR/ExportOptions.DeveloperID.plist"

TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
# The two channels differ only here: identity (bundle id), which tag the DMG
# publishes to, and the marketing version stamped into the app.
if [[ "$CHANNEL" == "stable" ]]; then
  BUNDLE_ID="ElevenIdeas.atc"
  # Release tags are created in CI, so sync them before resolving the latest.
  git -C "$REPO_ROOT" fetch --quiet origin 'refs/tags/v*:refs/tags/v*' 2>/dev/null || true
  TAG="$(cd "$REPO_ROOT" && git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=v:refname | tail -1)"
  [[ -n "$TAG" ]] || die "No vX.Y.Z tag found; run the stable App Server release first (mise run app-server:release:stable)"
  MARKETING_VERSION="${TAG#v}"
  TITLE=""
  DMG_BASENAME="atc-macos-arm64.dmg"
else
  BUNDLE_ID="ElevenIdeas.atc.dev"
  TAG="dev-$TIMESTAMP"
  MARKETING_VERSION=""
  TITLE="macOS App dev ($TIMESTAMP)"
  DMG_BASENAME="atc-dev-$TIMESTAMP.dmg"
fi

RUN_DIR="$REPO_ROOT/$ATC_ARTIFACT_ROOT/$TIMESTAMP"
ARCHIVE_PATH="$RUN_DIR/atc-$CHANNEL.xcarchive"
EXPORT_PATH="$RUN_DIR/export"
DERIVED_DATA_PATH="$RUN_DIR/DerivedData"
SOURCE_PACKAGES_PATH="$RUN_DIR/SourcePackages"
DMG_ROOT="$RUN_DIR/dmg-root"
DMG_PATH="$RUN_DIR/$DMG_BASENAME"
APP_PATH="$EXPORT_PATH/$APP_NAME.app"
LOG_DIR="$RUN_DIR/logs"

DEVELOPER_ID_IDENTITY=""

find_developer_id_identity() {
  local identities
  local line
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
    die "No valid Developer ID Application identity found for Team ID $ATC_TEAM_ID. Install the certificate and private key, then rerun."
  fi
}

validate_notary_profile() {
  local output
  if output="$(xcrun notarytool history --keychain-profile "$ATC_NOTARY_PROFILE" --no-progress 2>&1)"; then
    return
  fi

  printf '%s\n' "$output" >&2
  die "notarytool could not use stored-credentials profile '$ATC_NOTARY_PROFILE'. Unlock the default keychain or store credentials with 'xcrun notarytool store-credentials $ATC_NOTARY_PROFILE', then rerun."
}

validate_github_auth() {
  if [[ "$UPLOAD" -eq 1 ]]; then
    require_tool gh
    gh auth status -h github.com >/dev/null 2>&1 || die "gh is not authenticated for github.com. Run gh auth login, then rerun with --upload."
    if [[ "$CHANNEL" == "stable" ]]; then
      gh release view "$TAG" >/dev/null 2>&1 || die "No GitHub release for $TAG; run the stable App Server release first (mise run app-server:release:stable)."
    fi
  fi
}

validate_exported_app() {
  if [[ ! -d "$APP_PATH" ]]; then
    printf 'Expected exported app not found: %s\n' "$APP_PATH" >&2
    return 1
  fi

  local actual_bundle_id
  actual_bundle_id="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$APP_PATH/Contents/Info.plist")"
  if [[ "$actual_bundle_id" != "$BUNDLE_ID" ]]; then
    printf 'Expected bundle ID %s, got %s\n' "$BUNDLE_ID" "$actual_bundle_id" >&2
    return 1
  fi
}

# Successful release commands keep their complete output in per-step logs.
# Failures print that output before exiting; --verbose also streams it live.
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
      printf 'error: %s failed (full log: %s)\n' "$label" "$log_path" >&2
      return "$status"
    fi
  fi

  printf '  %-58s' "$label"
  if "$@" >"$log_path" 2>&1; then
    printf '✓\n'
    return
  else
    status=$?
    printf '✗\n\n' >&2
    printf '%s\n' "--- $label output ---" >&2
    cat "$log_path" >&2
    printf '%s\n' "--- end output ---" >&2
    printf 'error: %s failed (full log: %s)\n' "$label" "$log_path" >&2
    return "$status"
  fi
}

create_dmg() {
  ditto "$APP_PATH" "$DMG_ROOT/$APP_NAME.app" &&
  ln -s /Applications "$DMG_ROOT/Applications" &&
  hdiutil create \
    -volname "$APP_NAME" \
    -srcfolder "$DMG_ROOT" \
    -ov \
    -format UDZO \
    "$DMG_PATH"
}

sign_dmg() {
  codesign --force --sign "$DEVELOPER_ID_IDENTITY" "$DMG_PATH" &&
  codesign --verify --verbose=2 "$DMG_PATH"
}

staple_dmg() {
  xcrun stapler staple "$DMG_PATH" &&
  xcrun stapler validate "$DMG_PATH"
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

if [[ "$VERBOSE" -eq 1 ]]; then
  log "Validating signing, notarization, and upload prerequisites ($CHANNEL channel)"
else
  printf '\nmacOS release (%s)\n\n' "$CHANNEL"
  printf '  %-58s' "Validate signing, notarization, and upload prerequisites"
fi
find_developer_id_identity
validate_notary_profile
validate_github_auth
if [[ "$VERBOSE" -eq 0 ]]; then
  printf '✓\n'
fi

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
)
if [[ -n "$MARKETING_VERSION" ]]; then
  XCODE_OVERRIDES+=("MARKETING_VERSION=$MARKETING_VERSION")
fi

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
  "${XCODE_OVERRIDES[@]}"

run_step "Export Developer ID app" "03-export" xcodebuild -exportArchive \
  -archivePath "$ARCHIVE_PATH" \
  -exportPath "$EXPORT_PATH" \
  -exportOptionsPlist "$EXPORT_OPTIONS_PLIST" \
  -allowProvisioningUpdates

run_step "Validate exported app" "04-validate-app" validate_exported_app

run_step "Verify exported app signature" "05-verify-app" \
  codesign --verify --deep --strict --verbose=2 "$APP_PATH"

run_step "Create DMG" "06-create-dmg" create_dmg

run_step "Sign and verify DMG" "07-sign-dmg" sign_dmg

run_step "Submit DMG for notarization" "08-notarize" xcrun notarytool submit "$DMG_PATH" \
  --keychain-profile "$ATC_NOTARY_PROFILE" \
  --wait

run_step "Staple notarization ticket" "09-staple" staple_dmg

run_step "Assess DMG with Gatekeeper" "10-gatekeeper" \
  spctl --assess --type open --context context:primary-signature --verbose=4 "$DMG_PATH"

if [[ "$UPLOAD" -eq 1 ]]; then
  if [[ "$CHANNEL" == "stable" ]]; then
    run_step "Upload DMG to release $TAG" "11-upload" \
      gh release upload "$TAG" "$DMG_PATH" --clobber
  else
    run_step "Create GitHub prerelease $TAG" "11-upload" \
      gh release create "$TAG" "$DMG_PATH" \
      --title "$TITLE" \
      --notes "Developer ID notarized Apple Silicon dev build for $APP_NAME." \
      --prerelease
  fi
fi

log "Release artifact ready"
printf 'Channel: %s\n' "$CHANNEL"
printf 'Tag: %s\n' "$TAG"
printf 'DMG: %s\n' "$DMG_PATH"
printf 'Logs: %s\n' "$LOG_DIR"
