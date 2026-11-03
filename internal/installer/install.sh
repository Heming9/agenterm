#!/bin/sh
# Agenterm installer
# Project: https://github.com/Heming9/agenterm
# SPDX-License-Identifier: Apache-2.0
# License: https://www.apache.org/licenses/LICENSE-2.0
set -eu
version='@RELEASE_TAG@'
invitation=${1:-}
base=${2:-}
origin=${base%/downloads/*}
locale=${AGENTERM_LANG:-}
if [ -z "$locale" ] || [ "$locale" = auto ]; then
  locale=${LC_ALL:-${LC_MESSAGES:-${LANG:-}}}
  case "$locale" in C|POSIX|'') ;; *) if [ -n "${LANGUAGE:-}" ]; then locale=${LANGUAGE%%:*}; fi;; esac
fi
case "$locale" in '') if [ "$(uname -s)" = Darwin ]; then locale=$(defaults read -g AppleLanguages 2>/dev/null | tr -d '"(), ' | head -n 1 || true); fi;; esac
case "$locale" in zh_CN*|zh-SG*|zh_SG*|zh-CN*|zh-Hans*|zh_Hans*|zh|zh.*) chinese=1;; *) chinese=0;; esac
@TRANSLATIONS@
reset='' cyan='' teal='' muted='' gold='' green=''
if [ -t 1 ] && [ "${TERM:-dumb}" != dumb ] && [ -z "${NO_COLOR:-}" ]; then
  reset=$(printf '\033[0m')
  cyan=$(printf '\033[1;36m')
  teal=$(printf '\033[36m')
  muted=$(printf '\033[90m')
  gold=$(printf '\033[1;33m')
  green=$(printf '\033[1;32m')
fi
say() {
  color=''
  case "$1" in
    installer.connection.progress|installer.1.4.checking.connectivity.and.version|installer.2.4.preparing.client) color=$cyan ;;
    installer.server.reachable.invitation.and.version.verified|installer.cached.client.verified.skipping.download|installer.client.verified) color=$green ;;
  esac
  format=$(text "$1"); shift
  printf '%s' "$color"
  printf "$format\n" "$@"
  printf '%s' "$reset"
}
banner_row() {
  printf '%s│%s   %s%s%s  %s%s%s%s│%s\n' "$muted" "$reset" "$gold" "$1" "$reset" "$2" "$3" "$reset" "$muted" "$reset"
}
banner() {
  printf '\n%s╭──────────────────────────────────────────────────────╮%s\n' "$muted" "$reset"
  printf '%s│                                                      │%s\n' "$muted" "$reset"
  banner_row '▚▖     ▗▞' "$cyan" '▄▀█ █▀▀ █▀▀ █▄ █ ▀█▀ █▀▀ █▀█ █▀▄▀█      '
  banner_row ' ▸     ◂ ' "$teal" '█▀█ █▄█ ██▄ █ ▀█  █  ██▄ █▀▄ █ ▀ █      '
  banner_row '▞▘ ▄▄▄ ▝▚' '' "$(printf '%-40s' "Agenterm   [ $version ]")"
  printf '%s│                                                      │%s\n' "$muted" "$reset"
  printf '%s╰──────────────────────────────────────────────────────╯%s\n\n' "$muted" "$reset"
  say installer.project.s 'https://github.com/Heming9/agenterm'
  say installer.license
}
[ -n "$invitation" ] || { say installer.missing.invitation >&2; exit 1; }
[ -n "$base" ] || { say installer.missing.download.url >&2; exit 1; }
banner
printf '\n%s%s%s\n' "$gold" "$(text installer.remote.control.authorization)" "$reset"
say installer.server.s "$origin"
say installer.the.server.can.control.this.computer.with
printf '%s%s%s' "$gold" "$(text installer.allow.connection.y.n)" "$reset"
if ! read -r answer; then
  say installer.connection.cancelled
  exit 0
fi
case "$answer" in
  y|Y) ;;
  *) say installer.connection.cancelled; exit 0 ;;
esac
say installer.connection.progress
case "$(uname -s)" in Darwin) os=macos;; Linux) os=linux;; *) say installer.unsupported.system >&2; exit 1;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64;; x86_64|amd64) arch=amd64;; *) say installer.unsupported.architecture >&2; exit 1;; esac
[ -n "${HOME:-}" ] || { say installer.missing.home >&2; exit 1; }
case "$os" in
  macos) cache_root="${HOME}/Library/Caches/Agenterm" ;;
  linux) case "${XDG_CACHE_HOME:-}" in /*) cache_root="$XDG_CACHE_HOME/agenterm";; *) cache_root="${HOME}/.cache/agenterm";; esac ;;
esac
umask 077
cache="$cache_root/$version/$os-$arch"
mkdir -p "$cache"
work=$(mktemp -d "$cache/.tmp.XXXXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
say installer.1.4.checking.connectivity.and.version
if ! status=$(curl --http1.1 -sS --connect-timeout 5 --max-time 10 \
  -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  -H "Authorization: Bearer $invitation" \
  -H "X-Agenterm-Version: $version" \
  -D "$work/probe.headers" -o /dev/null -w '%{http_code}' "$origin/client?probe=1"); then
  say installer.cannot.reach.the.server.websocket.endpoint.check >&2; exit 1
fi
case "$status" in
  101) ;;
  401) say installer.invitation.invalid.expired.or.already.used.request >&2; exit 1 ;;
  429) say installer.too.many.authentication.failures.try.again.later >&2; exit 1 ;;
  426)
    server_version=$(awk '{ sub(/\r$/, ""); if (tolower($1) == "x-agenterm-version:") print $2 }' "$work/probe.headers")
    say installer.incompatible.versions.installer.s.server.s.update "$version" "${server_version:-unknown}" >&2; exit 1 ;;
  *) say installer.websocket.connectivity.check.failed.http.s.check "$status" >&2; exit 1 ;;
esac
awk '{ sub(/\r$/, ""); if (tolower($1) == "sec-websocket-accept:" && $2 == "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=") ok=1 } END { exit !ok }' "$work/probe.headers" || {
  say installer.invalid.websocket.handshake.response >&2; exit 1
}
say installer.server.reachable.invitation.and.version.verified
say installer.2.4.preparing.client
name="agenterm-client-cli-$os-$arch"
binary="$cache/$name"
curl -fsS --connect-timeout 5 --max-time 15 "$base/SHA256SUMS" -o "$work/SHA256SUMS"
checksum() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'; else sha256sum "$1" | awk '{print $1}'; fi
}
expected() {
  awk -v file="$1" '$2 == file && $1 ~ /^[[:xdigit:]]+$/ && length($1) == 64 {print tolower($1); exit}' "$work/SHA256SUMS"
}
expected_binary=$(expected "$name")
[ -n "$expected_binary" ] || { say installer.missing.client.checksum >&2; exit 1; }
if [ -f "$binary" ] && [ ! -L "$binary" ] && [ -x "$binary" ] && [ "$(checksum "$binary")" = "$expected_binary" ]; then
  say installer.cached.client.verified.skipping.download
else
  say installer.downloading.client
  archive_format=raw
  if command -v tar >/dev/null 2>&1; then
    printf '\375\067\172\130\132\000\000\004\346\326\264\106\002\000\041\001\014\000\000\000\217\230\101\234\340\003\377\000\013\135\000\000\157\375\377\377\243\267\177\106\062\000\000\000\014\047\151\040\227\143\170\303\000\001\047\200\010\000\000\000\056\171\256\255\261\304\147\373\002\000\000\000\000\004\131\132' > "$work/probe.tar.xz"
    if tar -tf "$work/probe.tar.xz" >/dev/null 2>&1; then
      archive_format=tar.xz
    else
      printf '\037\213\010\000\000\000\000\000\002\377\143\140\030\005\243\140\024\214\124\000\000\056\257\265\357\000\004\000\000' > "$work/probe.tar.gz"
      if tar -tf "$work/probe.tar.gz" >/dev/null 2>&1; then archive_format=tar.gz; fi
    fi
    rm -f "$work/probe.tar.xz" "$work/probe.tar.gz"
  fi
  asset="$name"
  if [ "$archive_format" != raw ]; then asset="$name.$archive_format"; fi
  expected_asset=$expected_binary
  if [ "$archive_format" != raw ]; then
    curl -fsS --connect-timeout 5 --max-time 30 "$base/$asset.sha256" -o "$work/archive.sha256"
    expected_asset=$(awk 'NR == 1 && $1 ~ /^[[:xdigit:]]+$/ && length($1) == 64 { print tolower($1) }' "$work/archive.sha256")
  fi
  [ -n "$expected_asset" ] || { say installer.missing.archive.checksum >&2; exit 1; }
  if [ -t 2 ]; then
    curl -fS --progress-bar --connect-timeout 5 --max-time 180 "$base/$asset" -o "$work/$asset"
  else
    curl -fsS --connect-timeout 5 --max-time 180 "$base/$asset" -o "$work/$asset"
  fi
  say installer.download.complete.verifying.and.extracting
  [ "$(checksum "$work/$asset")" = "$expected_asset" ] || { say installer.archive.checksum.mismatch >&2; exit 1; }
  if [ "$archive_format" != raw ]; then tar -xf "$work/$asset" -C "$work" "$name"; fi
  [ -f "$work/$name" ] && [ ! -L "$work/$name" ] || { say installer.invalid.client.archive >&2; exit 1; }
  [ "$(checksum "$work/$name")" = "$expected_binary" ] || { say installer.client.checksum.mismatch >&2; exit 1; }
  chmod 700 "$work/$name"
  [ ! -d "$binary" ] || { say installer.client.cache.path.is.a.directory >&2; exit 1; }
  mv -f "$work/$name" "$binary"
  say installer.client.verified
fi
"$binary" -invite "$invitation" -consent-confirmed
