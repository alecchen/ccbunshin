#!/usr/bin/env sh
set -eu

repo="${CCBUNSHIN_REPO:-alecchen/ccbunshin}"
version="${CCBUNSHIN_VERSION:-v0.0.3}"
prefix="${CCBUNSHIN_INSTALL_DIR:-$HOME/.local/bin}"

case "$(uname -s):$(uname -m)" in
  Linux:x86_64) asset="ccbunshin-linux-amd64" ;;
  Linux:aarch64|Linux:arm64) asset="ccbunshin-linux-arm64" ;;
  Darwin:x86_64) asset="ccbunshin-darwin-amd64" ;;
  Darwin:arm64) asset="ccbunshin-darwin-arm64" ;;
  *) echo "unsupported platform: $(uname -s) $(uname -m)" >&2; exit 1 ;;
esac

url="https://github.com/$repo/releases/download/$version/$asset"
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

mkdir -p "$prefix"
printf 'downloading %s\n' "$url"
curl -fsSL "$url" -o "$tmp"
chmod 755 "$tmp"
mv "$tmp" "$prefix/ccbunshin"
printf 'installed %s\n' "$prefix/ccbunshin"

case ":${PATH:-}:" in
  *:"$prefix":*) ;;
  *) printf 'add %s to PATH before using ccbunshin\n' "$prefix" ;;
esac
