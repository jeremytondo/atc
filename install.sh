#!/bin/sh
# ATC first-install script (ATC-261). Downloads the latest production
# release, verifies it against checksums.txt, and installs it to
# ~/.local/bin on macOS and Linux alike. First install only: after this,
# `atc upgrade` owns staying current (and `atc upgrade --dev` moves a
# machine onto the rolling dev build). Set ATC_VERSION=dev to install the
# rolling dev build directly on a new machine.
#
#   curl -fsSL https://raw.githubusercontent.com/jeremytondo/atc/main/install.sh | sh
#
# Knobs:
#   ATC_INSTALL_DIR  install directory (default: ~/.local/bin)
#   ATC_VERSION      release tag (e.g. immutable v0.1.0 or rolling dev)
set -eu

REPO="jeremytondo/atc"
INSTALL_DIR="${ATC_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
    echo "install.sh: $*" >&2
    exit 1
}

os=$(uname -s)
arch=$(uname -m)
case "$os/$arch" in
Darwin/arm64) platform="darwin_arm64" ;;
Linux/x86_64) platform="linux_amd64" ;;
Linux/aarch64 | Linux/arm64) platform="linux_arm64" ;;
Darwin/x86_64) fail "atc has no build for Intel macOS (darwin/amd64); supported: darwin/arm64, linux/amd64, linux/arm64" ;;
*) fail "atc has no build for $os/$arch; supported: darwin/arm64, linux/amd64, linux/arm64" ;;
esac

asset="atc_${platform}.tar.gz"
if [ -n "${ATC_VERSION:-}" ]; then
    base="https://github.com/$REPO/releases/download/$ATC_VERSION"
else
    base="https://github.com/$REPO/releases/latest/download"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "downloading $base/$asset"
curl -fsSL -o "$tmp/$asset" "$base/$asset" || fail "download failed: $base/$asset"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "download failed: $base/checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
    got=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
    got=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
else
    fail "neither sha256sum nor shasum is available to verify the download"
fi
want=$(awk -v asset="$asset" '$2 == asset { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "$asset has no entry in checksums.txt"
[ "$got" = "$want" ] || fail "checksum mismatch for $asset: checksums.txt says $want, the download is $got"

tar -xzf "$tmp/$asset" -C "$tmp" atc || fail "could not extract atc from $asset"
# Prove the downloaded binary runs on this machine before installing it; a
# bare `echo "$(...)"` would mask its failure.
version=$("$tmp/atc" version) || fail "downloaded binary failed to run"
[ -n "$version" ] || fail "downloaded binary printed no version"
mkdir -p "$INSTALL_DIR" || fail "cannot create $INSTALL_DIR"
mv -f "$tmp/atc" "$INSTALL_DIR/atc" || fail "cannot write to $INSTALL_DIR (this script never invokes sudo; set ATC_INSTALL_DIR to a writable directory)"

echo "installed atc $version to $INSTALL_DIR/atc"

case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*) echo "note: $INSTALL_DIR is not on your PATH; add it with e.g.  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
