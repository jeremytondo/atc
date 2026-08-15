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
