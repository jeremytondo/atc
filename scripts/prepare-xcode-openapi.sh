#!/usr/bin/env bash
set -euo pipefail

# Xcode 26 discovers the Swift OpenAPI Generator command for ATCKit but can
# omit it from the build graph while still compiling its declared outputs.
# Generate into the plugin's DerivedData directory first so clean Xcode builds
# remain deterministic. The build plugin then treats these files as current.

if [[ $# -ne 1 || -z "$1" ]]; then
  printf 'usage: %s <derived-data-path>\n' "$0" >&2
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd -P)"
DERIVED_DATA_PATH="$1"
GENERATED_SOURCES_PATH="$DERIVED_DATA_PATH/Build/Intermediates.noindex/BuildToolPluginIntermediates/atckit.output/ATCAppServerAPI/OpenAPIGenerator/GeneratedSources"

mkdir -p "$GENERATED_SOURCES_PATH"

swift run \
  --package-path "$REPO_ROOT/packages/ATCKit" \
  swift-openapi-generator generate \
  "$REPO_ROOT/packages/ATCKit/Sources/ATCAppServerAPI/openapi.json" \
  --config "$REPO_ROOT/packages/ATCKit/Sources/ATCAppServerAPI/openapi-generator-config.yaml" \
  --output-directory "$GENERATED_SOURCES_PATH" \
  --plugin-source build
