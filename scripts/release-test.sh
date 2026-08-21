#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd -P)"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/atc-release-test.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT

for script in "$SCRIPT_DIR"/*.sh; do
  bash -n "$script"
done

git -C "$TEST_ROOT" init -q
git -C "$TEST_ROOT" config user.name test
git -C "$TEST_ROOT" config user.email test@example.com
mkdir -p \
  "$TEST_ROOT/.github/workflows" \
  "$TEST_ROOT/app-server" \
  "$TEST_ROOT/macos" \
  "$TEST_ROOT/packages" \
  "$TEST_ROOT/scripts"
touch \
  "$TEST_ROOT/app-server/source.ts" \
  "$TEST_ROOT/app-server/openapi.json" \
  "$TEST_ROOT/macos/source.swift" \
  "$TEST_ROOT/packages/source.swift" \
  "$TEST_ROOT/mise.toml" \
  "$TEST_ROOT/.github/workflows/product-release.yml" \
  "$TEST_ROOT/scripts/ExportOptions.DeveloperID.plist" \
  "$TEST_ROOT/scripts/package-app-server.sh" \
  "$TEST_ROOT/scripts/prepare-xcode-openapi.sh" \
  "$TEST_ROOT/scripts/release-fingerprint.sh" \
  "$TEST_ROOT/scripts/release-macos.sh" \
  "$TEST_ROOT/scripts/release-plan.sh"
git -C "$TEST_ROOT" add .
git -C "$TEST_ROOT" commit -qm initial
git -C "$TEST_ROOT" tag v1.2.3
git -C "$TEST_ROOT" commit --allow-empty -qm second

stable="$(
  ATC_RELEASE_REPO_ROOT="$TEST_ROOT" \
    ATC_RELEASE_BUILT_AT="2026-08-15T12:34:56Z" \
    "$SCRIPT_DIR/release-plan.sh" stable minor
)"
[[ "$(awk -F= '$1 == "tag" { print $2 }' <<< "$stable")" == "v1.3.0" ]]
[[ "$(awk -F= '$1 == "kind" { print $2 }' <<< "$stable")" == "stable" ]]
[[ "$(awk -F= '$1 == "version" { print $2 }' <<< "$stable")" == "1.3.0" ]]
[[ "$(awk -F= '$1 == "marketing_version" { print $2 }' <<< "$stable")" == "1.3.0" ]]
[[ "$(awk -F= '$1 == "build_number" { print $2 }' <<< "$stable")" == "2" ]]
[[ "$(awk -F= '$1 == "built_at" { print $2 }' <<< "$stable")" == "2026-08-15T12:34:56Z" ]]

commit="$(git -C "$TEST_ROOT" rev-parse HEAD)"
dev="$(
  ATC_RELEASE_REPO_ROOT="$TEST_ROOT" \
    ATC_RELEASE_BUILT_AT="2026-08-15T12:34:56Z" \
    "$SCRIPT_DIR/release-plan.sh" dev
)"
[[ "$(awk -F= '$1 == "tag" { print $2 }' <<< "$dev")" == "dev" ]]
[[ "$(awk -F= '$1 == "kind" { print $2 }' <<< "$dev")" == "dev" ]]
[[ "$(awk -F= '$1 == "version" { print $2 }' <<< "$dev")" == "2026.8.15-dev.t123456+${commit:0:8}" ]]
[[ "$(awk -F= '$1 == "marketing_version" { print $2 }' <<< "$dev")" == "2026.8.15" ]]
[[ "$(awk -F= '$1 == "commit" { print $2 }' <<< "$dev")" == "$commit" ]]

candidate="$(
  ATC_RELEASE_REPO_ROOT="$TEST_ROOT" \
    ATC_RELEASE_BUILT_AT="2026-08-15T07:03:04Z" \
    "$SCRIPT_DIR/release-plan.sh" dev dev-pr-214
)"
[[ "$(awk -F= '$1 == "kind" { print $2 }' <<< "$candidate")" == "candidate" ]]
[[ "$(awk -F= '$1 == "tag" { print $2 }' <<< "$candidate")" == "dev-pr-214" ]]
[[ "$(awk -F= '$1 == "version" { print $2 }' <<< "$candidate")" == "2026.8.15-dev.t070304+${commit:0:8}" ]]

set +e
invalid_plan_output="$(
  ATC_RELEASE_REPO_ROOT="$TEST_ROOT" \
    ATC_RELEASE_BUILT_AT="2026-08-15 07:03:04" \
    "$SCRIPT_DIR/release-plan.sh" dev 2>&1
)"
invalid_plan_status=$?
set -e
[[ $invalid_plan_status -eq 1 ]]
[[ "$invalid_plan_output" == *"invalid release timestamp"* ]]

app_fingerprint="$(ATC_RELEASE_REPO_ROOT="$TEST_ROOT" "$SCRIPT_DIR/release-fingerprint.sh" app-server)"
mac_fingerprint="$(ATC_RELEASE_REPO_ROOT="$TEST_ROOT" "$SCRIPT_DIR/release-fingerprint.sh" macos)"
[[ "$app_fingerprint" =~ ^[0-9a-f]{64}$ && "$mac_fingerprint" =~ ^[0-9a-f]{64}$ ]]
[[ "$app_fingerprint" != "$mac_fingerprint" ]]

manifest="$TEST_ROOT/manifest.json"
"$SCRIPT_DIR/write-release-manifest.sh" \
  --kind dev \
  --tag dev \
  --version "2026.8.15-dev.t123456+${commit:0:8}" \
  --commit "$commit" \
  --built-at 2026-08-15T12:34:56Z \
  --app-server-fingerprint "$app_fingerprint" \
  --app-server-version "2026.8.15-dev.t123456+${commit:0:8}" \
  --app-server-commit "$commit" \
  --app-server-built-at 2026-08-15T12:34:56Z \
  --app-server-reused-from "" \
  --macos-fingerprint "$mac_fingerprint" \
  --macos-version 2026.8.14-dev.t221030+deadbeef \
  --macos-commit "$commit" \
  --macos-built-at 2026-08-14T22:10:30Z \
  --macos-reused-from dev \
  --output "$manifest"
jq -e '.schemaVersion == 1 and .release.tag == "dev" and .components.macos.reusedFrom == "dev"' "$manifest" >/dev/null

selection="$("$SCRIPT_DIR/select-release-components.sh" "$app_fingerprint" "$mac_fingerprint" "$manifest")"
[[ "$(awk -F= '$1 == "reuse_app_server" { print $2 }' <<< "$selection")" == "true" ]]
[[ "$(awk -F= '$1 == "app_server_source_tag" { print $2 }' <<< "$selection")" == "dev" ]]
[[ "$(awk -F= '$1 == "reuse_macos" { print $2 }' <<< "$selection")" == "true" ]]

miss_selection="$("$SCRIPT_DIR/select-release-components.sh" "$(printf app | shasum -a 256 | awk '{ print $1 }')" "$(printf mac | shasum -a 256 | awk '{ print $1 }')" "$manifest")"
[[ "$(awk -F= '$1 == "reuse_app_server" { print $2 }' <<< "$miss_selection")" == "false" ]]
[[ "$(awk -F= '$1 == "reuse_macos" { print $2 }' <<< "$miss_selection")" == "false" ]]

unsafe_manifest="$TEST_ROOT/unsafe-manifest.json"
jq '.components.appServer.version = "unsafe\noutput=true"' "$manifest" > "$unsafe_manifest"
unsafe_selection="$("$SCRIPT_DIR/select-release-components.sh" "$app_fingerprint" "$mac_fingerprint" "$unsafe_manifest")"
[[ "$(awk -F= '$1 == "reuse_app_server" { print $2 }' <<< "$unsafe_selection")" == "false" ]]
[[ "$(awk -F= '$1 == "reuse_macos" { print $2 }' <<< "$unsafe_selection")" == "true" ]]

mkdir -p "$TEST_ROOT/dist" "$TEST_ROOT/out"
cp /bin/sh "$TEST_ROOT/dist/atc-darwin-arm64"
cp /bin/sh "$TEST_ROOT/dist/atc-linux-x64"
chmod +x "$TEST_ROOT/dist/atc-darwin-arm64" "$TEST_ROOT/dist/atc-linux-x64"
"$SCRIPT_DIR/package-app-server.sh" \
  --dist "$TEST_ROOT/dist" \
  --out "$TEST_ROOT/out" \
  --checksums checksums-darwin.txt \
  darwin-arm64
"$SCRIPT_DIR/package-app-server.sh" \
  --dist "$TEST_ROOT/dist" \
  --out "$TEST_ROOT/out" \
  --checksums checksums-linux.txt \
  linux-x64
printf 'macOS disk image\n' > "$TEST_ROOT/out/atc-macos-arm64.dmg"
(
  cd "$TEST_ROOT/out"
  shasum -a 256 atc-macos-arm64.dmg > checksums-macos.txt
)
"$SCRIPT_DIR/merge-checksums.sh" "$TEST_ROOT/out"
[[ "$(wc -l < "$TEST_ROOT/out/checksums.txt" | tr -d ' ')" == "3" ]]
tar -tzf "$TEST_ROOT/out/atc-darwin-arm64.tar.gz" | grep -qx atc
tar -tzf "$TEST_ROOT/out/atc-linux-x64.tar.gz" | grep -qx atc
grep -q '  atc-macos-arm64.dmg$' "$TEST_ROOT/out/checksums.txt"
"$SCRIPT_DIR/verify-release-assets.sh" \
  --exact \
  "$TEST_ROOT/out/checksums.txt" \
  "$TEST_ROOT/out" \
  atc-darwin-arm64.tar.gz \
  atc-linux-x64.tar.gz \
  atc-macos-arm64.dmg

credential_dir="$TEST_ROOT/runner-temp/atc-release-credentials"
fake_bin="$TEST_ROOT/fake-bin"
security_log="$TEST_ROOT/security.log"
mkdir -p "$credential_dir" "$fake_bin"
printf '%s\n' \
  "$TEST_ROOT/Login Keychain.keychain-db" \
  "$TEST_ROOT/Secondary.keychain-db" > "$credential_dir/original-keychains"
printf '%s\n' "$TEST_ROOT/Login Keychain.keychain-db" > "$credential_dir/original-default-keychain"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf '\''%s\n'\'' "$*" >> "$SECURITY_LOG"' > "$fake_bin/security"
chmod +x "$fake_bin/security"
touch \
  "$credential_dir/release.keychain-db" \
  "$credential_dir/developer-id.p12" \
  "$credential_dir/AuthKey.p8" \
  "$credential_dir/unexpected-file"
PATH="$fake_bin:$PATH" \
  SECURITY_LOG="$security_log" \
  RUNNER_TEMP="$TEST_ROOT/runner-temp" \
  "$SCRIPT_DIR/cleanup-macos-signing.sh"
[[ ! -e "$credential_dir" ]]
grep -Fqx "list-keychains -d user -s $TEST_ROOT/Login Keychain.keychain-db $TEST_ROOT/Secondary.keychain-db" "$security_log"
grep -Fqx "default-keychain -d user -s $TEST_ROOT/Login Keychain.keychain-db" "$security_log"
grep -Fqx "delete-keychain $credential_dir/release.keychain-db" "$security_log"

release_fake_bin="$TEST_ROOT/release-fake-bin"
release_source="$TEST_ROOT/release-source"
mkdir -p "$release_fake_bin" "$release_source"
printf 'original asset\n' > "$release_source/atc-linux-x64.tar.gz"
(
  cd "$release_source"
  shasum -a 256 atc-linux-x64.tar.gz > checksums.txt
)
printf 'tampered asset\n' > "$release_source/atc-linux-x64.tar.gz"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'if [[ "$1" == "release" && "$2" == "download" ]]; then' \
  '  pattern=""' \
  '  out=""' \
  '  shift 2' \
  '  while [[ $# -gt 0 ]]; do' \
  '    case "$1" in' \
  '      --pattern) pattern="$2"; shift 2 ;;' \
  '      --dir) out="$2"; shift 2 ;;' \
  '      *) shift ;;' \
  '    esac' \
  '  done' \
  '  cp "$FAKE_RELEASE_SOURCE/$pattern" "$out/$pattern"' \
  '  exit 0' \
  'fi' \
  'if [[ "$1" == "pr" && "$2" == "view" ]]; then' \
  '  if [[ "$*" == *"--json state"* ]]; then' \
  '    [[ "$FAKE_PR_STATE" != "ERROR" ]] || exit 1' \
  '    printf '\''%s\n'\'' "$FAKE_PR_STATE"' \
  '    exit 0' \
  '  fi' \
  '  if [[ "$*" == *"--json headRefOid"* ]]; then' \
  '    printf '\''%s\n'\'' "$FAKE_PR_HEAD"' \
  '    exit 0' \
  '  fi' \
  'fi' \
  'exit 99' > "$release_fake_bin/gh"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'echo "unexpected git command: $*" >&2' \
  'exit 99' > "$release_fake_bin/git"
chmod +x "$release_fake_bin/gh" "$release_fake_bin/git"

set +e
reuse_output="$(
  PATH="$release_fake_bin:$PATH" \
    FAKE_RELEASE_SOURCE="$release_source" \
    "$SCRIPT_DIR/reuse-release-assets.sh" \
    --tag dev \
    --out "$TEST_ROOT/reused-assets" \
    --checksums checksums-reused.txt \
    atc-linux-x64.tar.gz 2>&1
)"
reuse_status=$?
set -e
[[ $reuse_status -eq 1 ]]
[[ "$reuse_output" == *"checksum mismatch"* ]]

candidate_assets="$TEST_ROOT/candidate-assets"
mkdir -p "$candidate_assets"
touch \
  "$candidate_assets/atc-darwin-arm64.tar.gz" \
  "$candidate_assets/atc-darwin-x64.tar.gz" \
  "$candidate_assets/atc-linux-arm64.tar.gz" \
  "$candidate_assets/atc-linux-x64.tar.gz" \
  "$candidate_assets/atc-macos-arm64.dmg" \
  "$candidate_assets/checksums.txt" \
  "$candidate_assets/manifest.json"

candidate_publish() {
  PATH="$release_fake_bin:$PATH" \
    FAKE_PR_STATE="$1" \
    FAKE_PR_HEAD="$2" \
    "$SCRIPT_DIR/publish-release.sh" \
    --kind candidate \
    --channel dev \
    --tag dev-pr-214 \
    --version "2026.8.15-dev.t070304+${commit:0:8}" \
    --commit "$commit" \
    --built-at 2026-08-15T07:03:04Z \
    --assets "$candidate_assets"
}

closed_output="$(candidate_publish CLOSED "$commit")"
[[ "$closed_output" == *"skipping candidate build for closed PR #214"* ]]

stale_head="0000000000000000000000000000000000000000"
stale_output="$(candidate_publish OPEN "$stale_head")"
[[ "$stale_output" == *"skipping obsolete candidate build $commit"* ]]

set +e
state_error_output="$(candidate_publish ERROR "$commit" 2>&1)"
state_error_status=$?
set -e
[[ $state_error_status -eq 1 ]]
[[ "$state_error_output" == *"could not resolve PR #214 state"* ]]

(
  cd "$candidate_assets"
  shasum -a 256 \
    atc-darwin-arm64.tar.gz \
    atc-darwin-x64.tar.gz \
    atc-linux-arm64.tar.gz \
    atc-linux-x64.tar.gz \
    manifest.json > checksums.txt
)
set +e
incomplete_checksums_output="$(candidate_publish OPEN "$commit" 2>&1)"
incomplete_checksums_status=$?
set -e
[[ $incomplete_checksums_status -eq 1 ]]
[[ "$incomplete_checksums_output" == *"does not include exactly one valid checksum for atc-macos-arm64.dmg"* ]]

set +e
missing_value_output="$($SCRIPT_DIR/package-app-server.sh --dist 2>&1)"
missing_value_status=$?
set -e
[[ $missing_value_status -eq 2 ]]
[[ "$missing_value_output" == *"missing value for --dist"* ]]

set +e
missing_value_output="$($SCRIPT_DIR/release-macos.sh --channel 2>&1)"
missing_value_status=$?
set -e
[[ $missing_value_status -eq 2 ]]
[[ "$missing_value_output" == *"missing value for --channel"* ]]

set +e
missing_pr_output="$("$SCRIPT_DIR/dev-build.sh" --pr 2>&1)"
missing_pr_status=$?
set -e
[[ $missing_pr_status -eq 2 ]]
[[ "$missing_pr_output" == *"usage: mise run dev-build"* ]]

set +e
invalid_timestamp_output="$(
  "$SCRIPT_DIR/release-macos.sh" \
    --channel dev \
    --version 1.2.3-dev.2 \
    --marketing-version 1.2.3 \
    --build-number 2 \
    --commit "$commit" \
    --built-at 2026-02-30T12:34:56Z \
    --output "$TEST_ROOT/atc.dmg" \
    2>&1
)"
invalid_timestamp_status=$?
set -e
[[ $invalid_timestamp_status -eq 1 ]]
[[ "$invalid_timestamp_output" == *"not a valid calendar timestamp"* ]]

printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf '\''%s\n'\'' "$4"' > "$fake_bin/date"
for tool in xcodebuild xcrun hdiutil codesign spctl ditto swift PlistBuddy; do
  printf '%s\n' '#!/usr/bin/env bash' 'exit 0' > "$fake_bin/$tool"
done
chmod +x "$fake_bin/date" "$fake_bin/xcodebuild" "$fake_bin/xcrun" \
  "$fake_bin/hdiutil" "$fake_bin/codesign" "$fake_bin/spctl" \
  "$fake_bin/ditto" "$fake_bin/swift" "$fake_bin/PlistBuddy"

run_macos_release_test() {
  local artifact_root="$1"
  local log_dir="$2"
  shift 2

  ATC_ARTIFACT_ROOT="$artifact_root" \
    ATC_PLIST_BUDDY="$fake_bin/PlistBuddy" \
    ATC_RELEASE_LOG_DIR="$log_dir" \
    "$SCRIPT_DIR/release-macos.sh" \
    --channel dev \
    --version 1.2.3-dev.2 \
    --marketing-version 1.2.3 \
    --build-number 2 \
    --commit "$commit" \
    --built-at 2026-08-15T12:34:56Z \
    --output "$TEST_ROOT/atc.dmg" \
    "$@"
}

prerequisite_log_dir="$TEST_ROOT/release-logs"
set +e
prerequisite_output="$(
  PATH="$fake_bin:$PATH" \
    SECURITY_LOG="$security_log" \
    run_macos_release_test "$TEST_ROOT/release-artifacts" "$prerequisite_log_dir" \
    2>&1
)"
prerequisite_status=$?
set -e
[[ $prerequisite_status -eq 1 ]]
[[ "$prerequisite_output" == *"Validate signing and notarization prerequisites"* ]]
grep -Fq "No valid Developer ID Application identity found" \
  "$prerequisite_log_dir/00-prerequisites.log"

tool_log="$TEST_ROOT/release-tools.log"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf '\''security %s\n'\'' "$*" >> "$TOOL_LOG"' \
  'if [[ "$*" == "find-identity -v -p codesigning" ]]; then' \
  '  printf '\''1) ABC "Developer ID Application: Test (337D6CNU4E)"\n'\''' \
  'fi' > "$fake_bin/security"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  '{ printf '\''xcrun'\''; for arg in "$@"; do printf '\'' <%s>'\'' "$arg"; done; printf '\''\n'\''; } >> "$TOOL_LOG"' > "$fake_bin/xcrun"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  '{ printf '\''xcodebuild'\''; for arg in "$@"; do printf '\'' <%s>'\'' "$arg"; done; printf '\''\n'\''; } >> "$TOOL_LOG"' \
  'exit 1' > "$fake_bin/xcodebuild"
chmod +x "$fake_bin/security" "$fake_bin/xcrun" "$fake_bin/xcodebuild"

api_key_path="$TEST_ROOT/AuthKey_TEST.p8"
touch "$api_key_path"
verbose_log_dir="$TEST_ROOT/verbose-release-logs"
set +e
verbose_output="$(
  PATH="$fake_bin:$PATH" \
    SECURITY_LOG="$security_log" \
    TOOL_LOG="$tool_log" \
    ATC_APP_STORE_CONNECT_KEY_PATH="$api_key_path" \
    ATC_APP_STORE_CONNECT_KEY_ID="KEY123" \
    ATC_APP_STORE_CONNECT_ISSUER_ID="ISSUER123" \
    run_macos_release_test "$TEST_ROOT/verbose-release-artifacts" "$verbose_log_dir" --verbose \
    2>&1
)"
verbose_status=$?
set -e
[[ $verbose_status -eq 1 ]]
[[ "$verbose_output" == *"Archive atc.app failed"* ]]
grep -Fq -- \
  "<CODE_SIGN_STYLE=Manual> <CODE_SIGN_IDENTITY=Developer ID Application: Test (337D6CNU4E)>" \
  "$tool_log"
if grep -Eq -- '-allowProvisioningUpdates|-authenticationKey(Path|ID|IssuerID)' "$tool_log"; then
  echo "release builds must not let Xcode create or update signing assets" >&2
  exit 1
fi
grep -Fq '<key>signingStyle</key>' "$REPO_ROOT/scripts/ExportOptions.DeveloperID.plist"
grep -Fq '<string>manual</string>' "$REPO_ROOT/scripts/ExportOptions.DeveloperID.plist"
grep -Fq '<key>signingCertificate</key>' "$REPO_ROOT/scripts/ExportOptions.DeveloperID.plist"
grep -Fq '<string>Developer ID Application</string>' "$REPO_ROOT/scripts/ExportOptions.DeveloperID.plist"

printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  '{ printf '\''xcodebuild'\''; for arg in "$@"; do printf '\'' <%s>'\'' "$arg"; done; printf '\''\n'\''; } >> "$TOOL_LOG"' \
  'if [[ "$1" == "-exportArchive" ]]; then' \
  '  while [[ $# -gt 0 ]]; do' \
  '    if [[ "$1" == "-exportPath" ]]; then mkdir -p "$2/atc.app/Contents"; exit; fi' \
  '    shift' \
  '  done' \
  'fi' > "$fake_bin/xcodebuild"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'case "$2" in' \
  '  *CFBundleIdentifier) printf '\''ElevenIdeas.atc.dev\n'\'' ;;' \
  '  *CFBundleShortVersionString) printf '\''1.2.3\n'\'' ;;' \
  '  *CFBundleVersion) printf '\''2\n'\'' ;;' \
  '  *ATCBuildVersion) printf '\''1.2.3-dev.2\n'\'' ;;' \
  '  *ATCBuildCommit) printf '\''%s\n'\'' "$EXPECTED_COMMIT" ;;' \
  '  *ATCBuildBuiltAt) printf '\''2026-08-15T12:34:56Z\n'\'' ;;' \
  '  *) exit 1 ;;' \
  'esac' > "$fake_bin/PlistBuddy"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'if [[ "$1" == "--display" ]]; then' \
  '  printf '\''Authority=Developer ID Application: Test (337D6CNU4E)\nTeamIdentifier=337D6CNU4E\n'\'' >&2' \
  'fi' > "$fake_bin/codesign"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'for path in "$@"; do :; done' \
  'touch "$path"' > "$fake_bin/hdiutil"
chmod +x "$fake_bin/xcodebuild" "$fake_bin/PlistBuddy" "$fake_bin/codesign" "$fake_bin/hdiutil"

: > "$tool_log"
PATH="$fake_bin:$PATH" \
  SECURITY_LOG="$security_log" \
  TOOL_LOG="$tool_log" \
  EXPECTED_COMMIT="$commit" \
  ATC_APP_STORE_CONNECT_KEY_PATH="$api_key_path" \
  ATC_APP_STORE_CONNECT_KEY_ID="KEY123" \
  ATC_APP_STORE_CONNECT_ISSUER_ID="ISSUER123" \
  run_macos_release_test "$TEST_ROOT/successful-release-artifacts" "$TEST_ROOT/successful-release-logs"
[[ -f "$TEST_ROOT/atc.dmg" ]]
grep -Fq '<-exportArchive>' "$tool_log"
if grep -Eq -- '-allowProvisioningUpdates|-authenticationKey(Path|ID|IssuerID)' "$tool_log"; then
  echo "successful release build mutated Xcode signing assets" >&2
  exit 1
fi

missing_key_path="$TEST_ROOT/missing-key.p8"
missing_key_log_dir="$TEST_ROOT/missing-key-logs"
set +e
missing_key_output="$(
  PATH="$fake_bin:$PATH" \
    SECURITY_LOG="$security_log" \
    TOOL_LOG="$tool_log" \
    ATC_APP_STORE_CONNECT_KEY_PATH="$missing_key_path" \
    ATC_APP_STORE_CONNECT_KEY_ID="KEY123" \
    ATC_APP_STORE_CONNECT_ISSUER_ID="ISSUER123" \
    run_macos_release_test "$TEST_ROOT/missing-key-artifacts" "$missing_key_log_dir" \
    2>&1
)"
missing_key_status=$?
set -e
[[ $missing_key_status -eq 1 ]]
[[ "$missing_key_output" == *"Validate signing and notarization prerequisites"* ]]
grep -Fq "App Store Connect API key not found: $missing_key_path" \
  "$missing_key_log_dir/00-prerequisites.log"

while IFS= read -r action; do
  if [[ ! "$action" =~ @[0-9a-f]{40}$ ]]; then
    echo "release workflow action is not pinned to a full commit SHA: $action" >&2
    exit 1
  fi
done < <(git -C "$REPO_ROOT" grep -hoE 'uses: [^ #]+' -- .github/workflows/product-*.yml)

if git -C "$REPO_ROOT" grep -qE 'secrets\.ATC_APP_STORE_CONNECT_(KEY_ID|ISSUER_ID)' -- .github/workflows/product-release.yml; then
  echo "non-secret App Store Connect identifiers must be environment variables" >&2
  exit 1
fi

if git -C "$REPO_ROOT" grep -qE 'ATC_NOTARY_KEY_' -- \
  .github/workflows/product-release.yml \
  scripts \
  ':(exclude)scripts/release-test.sh'; then
  echo "legacy misleading App Store Connect credential names remain" >&2
  exit 1
fi

git -C "$REPO_ROOT" grep -qE '^run-name:.*request_id' -- .github/workflows/product-release.yml
git -C "$REPO_ROOT" grep -qF 'shasum -a 256 atc-macos-arm64.dmg > checksums-macos.txt' -- .github/workflows/product-release.yml
git -C "$REPO_ROOT" grep -qF 'out/checksums-macos.txt' -- .github/workflows/product-release.yml
grep -q 'request_id=' "$REPO_ROOT/scripts/release.sh"
grep -q 'Product Release \[stable:$REQUEST_ID\]' "$REPO_ROOT/scripts/release.sh"
grep -q 'request_id=' "$REPO_ROOT/scripts/dev-build.sh"
grep -q 'mode=candidate' "$REPO_ROOT/scripts/dev-build.sh"
git -C "$REPO_ROOT" check-ignore -q --no-index AuthKey.p8
git -C "$REPO_ROOT" check-ignore -q --no-index another-key.p8

if git -C "$REPO_ROOT" grep -qE 'release:(dev|stable)|app-server:release|macos:release' -- mise.toml; then
  echo "legacy public release tasks remain in mise.toml" >&2
  exit 1
fi

echo "release tooling tests passed"
