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

if rg -q 'release:(dev|stable)|app-server:release|macos:release' "$REPO_ROOT/mise.toml"; then
  echo "legacy public release tasks remain in mise.toml" >&2
  exit 1
fi

echo "release tooling tests passed"
