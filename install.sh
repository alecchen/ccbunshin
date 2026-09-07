#!/usr/bin/env sh
set -eu

repo="${CCBUNSHIN_REPO:-alecchen/ccbunshin}"
version="${CCBUNSHIN_VERSION:-}"
prefix="${CCBUNSHIN_INSTALL_DIR:-$HOME/.local/bin}"

case "$(uname -s):$(uname -m)" in
  Linux:x86_64) asset="ccbunshin-linux-amd64" ;;
  Linux:aarch64|Linux:arm64) asset="ccbunshin-linux-arm64" ;;
  Darwin:x86_64) asset="ccbunshin-darwin-amd64" ;;
  Darwin:arm64) asset="ccbunshin-darwin-arm64" ;;
  *) echo "unsupported platform: $(uname -s) $(uname -m)" >&2; exit 1 ;;
esac

# Without CCBUNSHIN_VERSION the stable "latest" endpoint always resolves to the
# newest GitHub Release, so cutting a release needs no change to this script.
if [ -n "$version" ]; then
  url="https://github.com/$repo/releases/download/$version/$asset"
else
  url="https://github.com/$repo/releases/latest/download/$asset"
fi

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
