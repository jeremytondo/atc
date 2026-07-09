#!/bin/sh
set -eu

repo="${ATC_REPO:-jeremytondo/atelier-code}"
install_dir="${ATC_INSTALL_DIR:-$HOME/.local/bin}"
version="${ATC_VERSION:-latest}"

usage() {
  cat <<'EOF'
Install Atelier Code from GitHub Releases.

Usage:
  ./install.sh [--version vX.Y.Z]

Environment:
  ATC_REPO         GitHub repository (default: jeremytondo/atelier-code)
  ATC_INSTALL_DIR  Install directory (default: ~/.local/bin)
  ATC_VERSION      Release tag, or latest (default: latest)
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --help|-h)
      usage
      exit 0
      ;;
    --version)
      if [ "$#" -lt 2 ]; then
        echo "missing value for --version" >&2
        exit 1
      fi
      version="$2"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "required command not found: $1" >&2
    exit 1
  fi
}

need curl
need tar
need mktemp
need install

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  linux|darwin) ;;
  *)
    echo "unsupported OS: $os" >&2
    exit 1
    ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *)
    echo "unsupported architecture: $arch" >&2
    exit 1
    ;;
esac

archive="atc_${os}_${arch}.tar.gz"
if [ "$version" = "latest" ]; then
  download_base="https://github.com/${repo}/releases/latest/download"
else
  download_base="https://github.com/${repo}/releases/download/${version}"
fi

tmp="$(mktemp -d)"
cleanup() {
  rm -rf "$tmp"
}
trap cleanup EXIT HUP INT TERM

curl -fsSL "${download_base}/${archive}" -o "${tmp}/${archive}"
curl -fsSL "${download_base}/checksums.txt" -o "${tmp}/checksums.txt"

expected="$(awk -v file="$archive" '{ name=$NF; sub(/^\*/, "", name); if (name == file) print $1 }' "$tmp/checksums.txt")"
if [ -z "$expected" ]; then
  echo "checksums.txt does not include ${archive}" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$archive" | awk '{ print $1 }')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')"
else
  echo "required command not found: sha256sum or shasum" >&2
  exit 1
fi

if [ "$actual" != "$expected" ]; then
  echo "checksum mismatch for ${archive}" >&2
  echo "expected: ${expected}" >&2
  echo "actual:   ${actual}" >&2
  exit 1
fi

tar -xzf "$tmp/$archive" -C "$tmp"
mkdir -p "$install_dir"
install -m 0755 "$tmp/atc" "$install_dir/atc"

echo "installed atc to ${install_dir}/atc"
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *)
    echo "add ${install_dir} to PATH to run atc directly"
    ;;
esac

