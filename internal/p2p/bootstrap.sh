#!/bin/sh
set -eu
umask 077
case "$(uname -s)" in Darwin) platform=darwin ;; Linux) platform=linux ;; *) echo 'Unsupported p2p-cli platform' >&2; exit 1 ;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) echo 'Unsupported p2p-cli architecture' >&2; exit 1 ;; esac
case "$platform/$arch" in
@ASSET_CASES@
  *) echo 'No p2p-cli artifact for this platform' >&2; exit 1 ;;
esac
cache="${XDG_CACHE_HOME:-$HOME/.cache}/agenterm/p2p-cli/@VERSION@-$expected"
bin="$cache/p2p-cli"
if [ '@MODE@' = stop ]; then
  if [ -x "$bin" ]; then "$bin" stop --session '@SESSION@'; fi
  exit 0
fi
mkdir -p "$cache"
chmod 700 "$cache"
hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d ' ' -f 1
  else shasum -a 256 "$1" | cut -d ' ' -f 1
  fi
}
# A private, verified archive is the cache authority, not just an executable's existence.
if [ ! -f "$cache/$archive" ] || [ "$(hash_file "$cache/$archive")" != "$expected" ] || [ ! -x "$bin" ]; then
  tmp=$(mktemp -d "$cache/download.XXXXXXXX")
  trap 'rm -rf "$tmp"' EXIT HUP INT TERM
  curl -fLsS --connect-timeout 10 --max-time 120 "$url" -o "$tmp/$archive"
  [ "$(hash_file "$tmp/$archive")" = "$expected" ] || { echo 'p2p-cli checksum mismatch' >&2; exit 1; }
  tar -xzf "$tmp/$archive" -C "$tmp" p2p-cli
  chmod 700 "$tmp/p2p-cli"
  mv "$tmp/p2p-cli" "$bin"
  mv "$tmp/$archive" "$cache/$archive"
  rm -rf "$tmp"
  trap - EXIT HUP INT TERM
fi
config=${AGENTERM_P2P_CONFIG_B64:-@CONFIG_B64@}
[ -n "$config" ] || { echo 'Missing P2P negotiation parameters' >&2; exit 1; }
if [ '@MODE@' = foreground ]; then
  printf '%s' "$config" | base64 -d | "$bin" start --config - --foreground
else
  printf '%s' "$config" | base64 -d | "$bin" start --config -
fi
