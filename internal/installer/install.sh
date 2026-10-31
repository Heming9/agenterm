#!/bin/sh
set -eu
version='@RELEASE_TAG@'
invitation=${1:?missing invitation}
case "$(uname -s)" in Darwin) os=macos;; Linux) os=linux;; *) echo 'Unsupported system' >&2; exit 1;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64;; x86_64|amd64) arch=amd64;; *) echo 'Unsupported architecture' >&2; exit 1;; esac
base="https://github.com/Heming9/agenterm/releases/download/$version"
case "$os" in
  macos) cache_root="${HOME:?missing HOME}/Library/Caches/Agenterm" ;;
  linux) case "${XDG_CACHE_HOME:-}" in /*) cache_root="$XDG_CACHE_HOME/agenterm";; *) cache_root="${HOME:?missing HOME}/.cache/agenterm";; esac ;;
esac
umask 077
cache="$cache_root/$version/$os-$arch"
mkdir -p "$cache"
work=$(mktemp -d "$cache/.tmp.XXXXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
asset="agenterm-cli-$os-$arch.tar.gz"
name="agenterm-cli-$os-$arch"
binary="$cache/$name"
curl -fsSL "$base/SHA256SUMS" -o "$work/SHA256SUMS"
checksum() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'; else sha256sum "$1" | awk '{print $1}'; fi
}
expected() {
  awk -v file="$1" '$2 == file && $1 ~ /^[[:xdigit:]]+$/ && length($1) == 64 {print tolower($1); exit}' "$work/SHA256SUMS"
}
expected_binary=$(expected "$name")
[ -n "$expected_binary" ] || { echo 'Missing client checksum' >&2; exit 1; }
if [ -f "$binary" ] && [ ! -L "$binary" ] && [ -x "$binary" ] && [ "$(checksum "$binary")" = "$expected_binary" ]; then
  echo 'Using verified cached client.'
else
  echo 'Downloading client.'
  expected_asset=$(expected "$asset")
  [ -n "$expected_asset" ] || { echo 'Missing archive checksum' >&2; exit 1; }
  curl -fsSL "$base/$asset" -o "$work/$asset"
  [ "$(checksum "$work/$asset")" = "$expected_asset" ] || { echo 'Archive checksum mismatch' >&2; exit 1; }
  tar -xzf "$work/$asset" -C "$work"
  [ -f "$work/$name" ] && [ ! -L "$work/$name" ] || { echo 'Invalid client archive' >&2; exit 1; }
  [ "$(checksum "$work/$name")" = "$expected_binary" ] || { echo 'Client checksum mismatch' >&2; exit 1; }
  chmod 700 "$work/$name"
  [ ! -d "$binary" ] || { echo 'Client cache path is a directory' >&2; exit 1; }
  mv -f "$work/$name" "$binary"
fi
"$binary" -invite "$invitation"
