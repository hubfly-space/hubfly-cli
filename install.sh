#!/usr/bin/env bash
set -euo pipefail

REPO_OWNER="hubfly-space"
REPO_NAME="hubfly-cli"
BINARY_NAME="hubfly"

uname_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
uname_arch="$(uname -m)"

case "$uname_os" in
  linux) os="linux" ;;
  darwin) os="darwin" ;;
  *)
    echo "Unsupported OS: $uname_os"
    exit 1
    ;;
esac

case "$uname_arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *)
    echo "Unsupported architecture: $uname_arch"
    exit 1
    ;;
esac

asset="hubfly_${os}_${arch}.tar.gz"
url="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/latest/download/${asset}"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

echo "Downloading ${asset}..."
curl -fsSL "$url" -o "$tmp_dir/$asset"

echo "Extracting ${asset}..."
tar -xzf "$tmp_dir/$asset" -C "$tmp_dir"

if [[ ! -f "$tmp_dir/$BINARY_NAME" ]]; then
  echo "Binary $BINARY_NAME not found in archive"
  exit 1
fi

chmod +x "$tmp_dir/$BINARY_NAME"

install_dir="/usr/local/bin"
if [[ ! -w "$install_dir" ]]; then
  install_dir="$HOME/.local/bin"
  mkdir -p "$install_dir"
fi

if [[ -w "$install_dir" ]]; then
  install "$tmp_dir/$BINARY_NAME" "$install_dir/$BINARY_NAME"
else
  echo "No write access to ${install_dir}. Try running with sudo."
  exit 1
fi

echo "Installed ${BINARY_NAME} to ${install_dir}/${BINARY_NAME}"
if ! command -v hubfly >/dev/null 2>&1; then
  echo "Add ${install_dir} to your PATH, then run: hubfly version"
else
  echo "Run: hubfly version"
fi
