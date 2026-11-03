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
# The source executable hash remains authoritative for every download format.
if [ ! -x "$bin" ] || [ "$(hash_file "$bin")" != "$expected" ]; then
  tmp=$(mktemp -d "$cache/download.XXXXXXXX")
  trap 'rm -rf "$tmp"' EXIT HUP INT TERM
  format=raw
  if command -v tar >/dev/null 2>&1; then
    printf '\375\067\172\130\132\000\000\004\346\326\264\106\002\000\041\001\014\000\000\000\217\230\101\234\340\003\377\000\013\135\000\000\157\375\377\377\243\267\177\106\062\000\000\000\014\047\151\040\227\143\170\303\000\001\047\200\010\000\000\000\056\171\256\255\261\304\147\373\002\000\000\000\000\004\131\132' > "$tmp/probe.tar.xz"
    if tar -tf "$tmp/probe.tar.xz" >/dev/null 2>&1; then
      format=tar.xz
    else
      printf '\037\213\010\000\000\000\000\000\002\377\143\140\030\005\243\140\024\214\124\000\000\056\257\265\357\000\004\000\000' > "$tmp/probe.tar.gz"
      if tar -tf "$tmp/probe.tar.gz" >/dev/null 2>&1; then format=tar.gz; fi
    fi
    rm -f "$tmp/probe.tar.xz" "$tmp/probe.tar.gz"
  fi
  archive="$source"
  if [ "$format" != raw ]; then archive="$source.$format"; fi
  url="${url%/*}/$archive"
  curl -fLsS --connect-timeout 10 --max-time 120 "$url" -o "$tmp/$archive"
  if [ "$format" != raw ]; then
    curl -fLsS --connect-timeout 10 --max-time 30 "$url.sha256" -o "$tmp/archive.sha256"
    archive_hash=$(awk 'NR == 1 && $1 ~ /^[[:xdigit:]]+$/ && length($1) == 64 { print tolower($1) }' "$tmp/archive.sha256")
    [ -n "$archive_hash" ] && [ "$(hash_file "$tmp/$archive")" = "$archive_hash" ] || { echo 'p2p-cli archive checksum mismatch' >&2; exit 1; }
    tar -xf "$tmp/$archive" -C "$tmp" p2p-cli
  else
    mv "$tmp/$source" "$tmp/p2p-cli"
  fi
  [ "$(hash_file "$tmp/p2p-cli")" = "$expected" ] || { echo 'p2p-cli checksum mismatch' >&2; exit 1; }
  chmod 700 "$tmp/p2p-cli"
  mv "$tmp/p2p-cli" "$bin"
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
