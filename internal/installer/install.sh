#!/bin/sh
set -eu
version='@RELEASE_TAG@'
invitation=${1:?missing invitation}
base=${2:?missing download URL}
origin=${base%/downloads/*}
case "$(uname -s)" in Darwin) os=macos;; Linux) os=linux;; *) echo 'Unsupported system' >&2; exit 1;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64;; x86_64|amd64) arch=amd64;; *) echo 'Unsupported architecture' >&2; exit 1;; esac
case "$os" in
  macos) cache_root="${HOME:?missing HOME}/Library/Caches/Agenterm" ;;
  linux) case "${XDG_CACHE_HOME:-}" in /*) cache_root="$XDG_CACHE_HOME/agenterm";; *) cache_root="${HOME:?missing HOME}/.cache/agenterm";; esac ;;
esac
umask 077
cache="$cache_root/$version/$os-$arch"
mkdir -p "$cache"
work=$(mktemp -d "$cache/.tmp.XXXXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
printf '\n连接进度\n[1/5] 检查连通性与版本…\n'
if ! status=$(curl --http1.1 -sS --connect-timeout 5 --max-time 10 \
  -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  -H "Authorization: Bearer $invitation" \
  -H "X-Agenterm-Version: $version" \
  -D "$work/probe.headers" -o /dev/null -w '%{http_code}' "$origin/client?probe=1"); then
  echo 'Cannot reach the server WebSocket endpoint. Check the address, network, and proxy configuration.' >&2; exit 1
fi
case "$status" in
  101) ;;
  401) echo 'Invitation invalid, expired, or already used. Request a new invitation.' >&2; exit 1 ;;
  429) echo 'Too many authentication failures. Try again later.' >&2; exit 1 ;;
  426)
    server_version=$(awk '{ sub(/\r$/, ""); if (tolower($1) == "x-agenterm-version:") print $2 }' "$work/probe.headers")
    echo "版本不兼容：安装器 ${version}，Server ${server_version:-未知}。请将 Server 和 Client 更新到兼容版本。" >&2; exit 1 ;;
  *) echo "WebSocket connectivity check failed (HTTP $status). Check the server and reverse proxy configuration." >&2; exit 1 ;;
esac
awk '{ sub(/\r$/, ""); if (tolower($1) == "sec-websocket-accept:" && $2 == "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=") ok=1 } END { exit !ok }' "$work/probe.headers" || {
  echo 'Invalid WebSocket handshake response.' >&2; exit 1
}
echo '      服务可达，邀请和版本校验通过。'
echo '[2/5] 准备客户端…'
asset="agenterm-cli-$os-$arch.tar.gz"
name="agenterm-cli-$os-$arch"
binary="$cache/$name"
curl -fsS --connect-timeout 5 --max-time 15 "$base/SHA256SUMS" -o "$work/SHA256SUMS"
checksum() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'; else sha256sum "$1" | awk '{print $1}'; fi
}
expected() {
  awk -v file="$1" '$2 == file && $1 ~ /^[[:xdigit:]]+$/ && length($1) == 64 {print tolower($1); exit}' "$work/SHA256SUMS"
}
expected_binary=$(expected "$name")
[ -n "$expected_binary" ] || { echo 'Missing client checksum' >&2; exit 1; }
if [ -f "$binary" ] && [ ! -L "$binary" ] && [ -x "$binary" ] && [ "$(checksum "$binary")" = "$expected_binary" ]; then
  echo '      本地客户端校验通过，跳过下载。'
else
  echo '      正在下载客户端…'
  expected_asset=$(expected "$asset")
  [ -n "$expected_asset" ] || { echo 'Missing archive checksum' >&2; exit 1; }
  if [ -t 2 ]; then
    curl -fS --progress-bar --connect-timeout 5 --max-time 180 "$base/$asset" -o "$work/$asset"
  else
    curl -fsS --connect-timeout 5 --max-time 180 "$base/$asset" -o "$work/$asset"
  fi
  echo '      下载完成，正在校验并解压…'
  [ "$(checksum "$work/$asset")" = "$expected_asset" ] || { echo 'Archive checksum mismatch' >&2; exit 1; }
  tar -xzf "$work/$asset" -C "$work"
  [ -f "$work/$name" ] && [ ! -L "$work/$name" ] || { echo 'Invalid client archive' >&2; exit 1; }
  [ "$(checksum "$work/$name")" = "$expected_binary" ] || { echo 'Client checksum mismatch' >&2; exit 1; }
  chmod 700 "$work/$name"
  [ ! -d "$binary" ] || { echo 'Client cache path is a directory' >&2; exit 1; }
  mv -f "$work/$name" "$binary"
  echo '      客户端校验通过。'
fi
"$binary" -invite "$invitation"
