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
touch "$TEST_ROOT/file"
git -C "$TEST_ROOT" add file
git -C "$TEST_ROOT" commit -qm initial
git -C "$TEST_ROOT" tag v1.2.3
git -C "$TEST_ROOT" commit --allow-empty -qm second

stable="$(
  ATC_RELEASE_REPO_ROOT="$TEST_ROOT" \
    ATC_RELEASE_BUILT_AT="2026-08-15T12:34:56Z" \
    "$SCRIPT_DIR/release-plan.sh" stable minor
)"
[[ "$(awk -F= '$1 == "tag" { print $2 }' <<< "$stable")" == "v1.3.0" ]]
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
[[ "$(awk -F= '$1 == "version" { print $2 }' <<< "$dev")" == "1.2.4-dev.2+${commit:0:12}" ]]
[[ "$(awk -F= '$1 == "commit" { print $2 }' <<< "$dev")" == "$commit" ]]

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
"$SCRIPT_DIR/merge-checksums.sh" "$TEST_ROOT/out"
[[ "$(wc -l < "$TEST_ROOT/out/checksums.txt" | tr -d ' ')" == "2" ]]
tar -tzf "$TEST_ROOT/out/atc-darwin-arm64.tar.gz" | grep -qx atc
tar -tzf "$TEST_ROOT/out/atc-linux-x64.tar.gz" | grep -qx atc

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
  'printf '\''xcrun %s\n'\'' "$*" >> "$TOOL_LOG"' > "$fake_bin/xcrun"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf '\''xcodebuild %s\n'\'' "$*" >> "$TOOL_LOG"' \
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
  "-authenticationKeyPath $api_key_path -authenticationKeyID KEY123 -authenticationKeyIssuerID ISSUER123" \
  "$tool_log"

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
done < <(git -C "$REPO_ROOT" grep -hoE 'uses: [^ #]+' -- .github/workflows/product-release.yml)

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
git -C "$REPO_ROOT" grep -q 'request_id=' -- scripts/release.sh
git -C "$REPO_ROOT" check-ignore -q --no-index AuthKey.p8
git -C "$REPO_ROOT" check-ignore -q --no-index another-key.p8

if git -C "$REPO_ROOT" grep -qE 'release:(dev|stable)|app-server:release|macos:release' -- mise.toml; then
  echo "legacy public release tasks remain in mise.toml" >&2
  exit 1
fi

echo "release tooling tests passed"
