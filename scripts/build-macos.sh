#!/usr/bin/env bash
# Incremental, non-publishing developer build of the native macOS app.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd -P)"
DERIVED_DATA_PATH="${ATC_MACOS_DERIVED_DATA_PATH:-$REPO_ROOT/.build/macos/DerivedData}"
SOURCE_PACKAGES_PATH="${ATC_XCODE_SPM_DIR:-$REPO_ROOT/.build/macos/SourcePackages}"

mkdir -p "$DERIVED_DATA_PATH" "$SOURCE_PACKAGES_PATH"
"$SCRIPT_DIR/prepare-xcode-openapi.sh" "$DERIVED_DATA_PATH"
xcodebuild build \
  -project "$REPO_ROOT/macos/atc.xcodeproj" \
  -scheme atc \
  -configuration Debug \
  -destination platform=macOS \
  -derivedDataPath "$DERIVED_DATA_PATH" \
  -clonedSourcePackagesDirPath "$SOURCE_PACKAGES_PATH" \
  -skipPackagePluginValidation \
  CODE_SIGNING_ALLOWED=NO

printf 'built %s\n' "$DERIVED_DATA_PATH/Build/Products/Debug/atc.app"
