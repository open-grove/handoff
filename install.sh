#!/bin/sh
set -eu

repository="open-grove/handoff"
system="$(uname -s | tr '[:upper:]' '[:lower:]')"
machine="$(uname -m)"
case "$machine" in
  x86_64|amd64) architecture="amd64" ;;
  arm64|aarch64) architecture="arm64" ;;
  *) echo "Unsupported architecture: $machine" >&2; exit 1 ;;
esac
case "$system" in
  darwin|linux) ;;
  *) echo "Use the Windows release asset on $system." >&2; exit 1 ;;
esac

asset="handoff_${system}_${architecture}.tar.gz"
install_dir="${HANDOFF_INSTALL_DIR:-$HOME/.local/bin}"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT INT TERM

if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  gh release download --repo "$repository" --pattern "$asset" --pattern SHA256SUMS --dir "$work_dir"
else
  base_url="https://github.com/${repository}/releases/latest/download"
  curl -fL --retry 3 -o "$work_dir/$asset" "$base_url/$asset"
  curl -fL --retry 3 -o "$work_dir/SHA256SUMS" "$base_url/SHA256SUMS"
fi

expected="$(awk -v name="$asset" '$2 == name { print $1 }' "$work_dir/SHA256SUMS")"
test -n "$expected"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$work_dir/$asset" | awk '{ print $1 }')"
else
  actual="$(shasum -a 256 "$work_dir/$asset" | awk '{ print $1 }')"
fi
test "$expected" = "$actual"

tar -xzf "$work_dir/$asset" -C "$work_dir"
mkdir -p "$install_dir"
chmod 0755 "$work_dir/handoff"
mv "$work_dir/handoff" "$install_dir/handoff"
echo "Installed handoff to $install_dir/handoff"
echo "Run: handoff skills install"
