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

download_asset() {
  name="$1"
  destination="$2"
  if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    repo_token="$(gh auth token)"
    api_url="$(gh api "repos/$repository/releases/latest" --jq ".assets[] | select(.name == \"$name\") | .url")"
    test -n "$api_url"
    curl -fsSL --retry 3 --connect-timeout 10 --max-time 180 \
      -H "Authorization: Bearer $repo_token" \
      -H "Accept: application/octet-stream" \
      -H "X-GitHub-Api-Version: 2022-11-28" \
      -o "$destination" "$api_url"
  else
    curl -fsSL --retry 3 --connect-timeout 10 --max-time 180 \
      -o "$destination" "https://github.com/${repository}/releases/latest/download/$name"
  fi
}

download_asset "$asset" "$work_dir/$asset"
download_asset SHA256SUMS "$work_dir/SHA256SUMS"

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
if "$install_dir/handoff" skills install >/dev/null 2>&1; then
  echo "Installed the matching Handoff Skill for Codex, Claude Code, and compatible Agents"
else
  echo "Existing customized Skill was preserved" >&2
  echo "To replace it intentionally: handoff skills install --force" >&2
fi
