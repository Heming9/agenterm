#!/bin/sh
# Agenterm installer
# Project: https://github.com/Heming9/agenterm
# SPDX-License-Identifier: Apache-2.0
# License: https://www.apache.org/licenses/LICENSE-2.0
set -eu
version='@RELEASE_TAG@'
token=${1:-}
base=${2:-}
locale=${AGENTERM_LANG:-}
if [ -z "$locale" ] || [ "$locale" = auto ]; then
  locale=${LC_ALL:-${LC_MESSAGES:-${LANG:-}}}
  case "$locale" in C|POSIX|'') ;; *) if [ -n "${LANGUAGE:-}" ]; then locale=${LANGUAGE%%:*}; fi;; esac
fi
case "$locale" in '') if [ "$(uname -s)" = Darwin ]; then locale=$(defaults read -g AppleLanguages 2>/dev/null | tr -d '"(), ' | head -n 1 || true); fi;; esac
case "$locale" in zh_CN*|zh-SG*|zh_SG*|zh-CN*|zh-Hans*|zh_Hans*|zh|zh.*) chinese=1;; *) chinese=0;; esac
@TRANSLATIONS@
reset='' primary='' success=''
if [ -t 1 ] && [ "${TERM:-dumb}" != dumb ] && [ -z "${NO_COLOR:-}" ]; then
  reset=$(printf '\033[0m')
  case "${COLORTERM:-}:${TERM_PROGRAM:-}:${WT_SESSION:-}" in
    truecolor:*|24bit:*|*:Apple_Terminal:*|*:vscode:*|*:*:?*) primary=$(printf '\033[@COLOR_PRIMARY_SGR@m') ;;
    *) primary=$(printf '\033[1;@COLOR_PRIMARY@m') ;;
  esac
  success=$(printf '\033[1;@COLOR_SUCCESS@m')
fi
say() {
  color=''
  case "$1" in
    installer.2.4.preparing.client) color=$primary ;;
    installer.cached.client.verified.skipping.download|installer.client.verified) color=$success ;;
  esac
  format=$(text "$1"); shift
  printf '%s' "$color"
  printf "$format\n" "$@"
  printf '%s' "$reset"
}
banner() {
  printf '\n%s  @LOGO_TOP@  %s\n' "$primary" '@WORDMARK_TOP@'
  printf '  @LOGO_MIDDLE@   %s\n' '@WORDMARK_BOTTOM@'
  printf '  @LOGO_BOTTOM@  Agenterm   [ %s ]%s\n\n' "$version" "$reset"
  say installer.project.s 'https://github.com/Heming9/agenterm'
  say installer.license
}
[ -n "$base" ] || { say installer.missing.download.url >&2; exit 1; }
banner
case "$(uname -s)" in Darwin) os=macos;; Linux) os=linux;; *) say installer.unsupported.system >&2; exit 1;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64;; x86_64|amd64) arch=amd64;; *) say installer.unsupported.architecture >&2; exit 1;; esac
umask 077
temp_root=${TMPDIR:-}
if [ -z "$temp_root" ] && [ "$os" = macos ]; then
  temp_root=$(getconf DARWIN_USER_TEMP_DIR 2>/dev/null || true)
fi
temp_root=${temp_root:-/tmp}
case "$temp_root" in /*) ;; *) say installer.invalid.temp.directory >&2; exit 1;; esac
work=$(mktemp -d "$temp_root/agenterm-assistance.XXXXXXXX")
trap 'rm -rf "$work"' EXIT
trap 'exit 130' HUP INT TERM
say installer.2.4.preparing.client
name="agenterm-client-cli-$os-$arch"
binary="$work/$name"
download_asset() {
  timeout=$1
  shift
  curl --connect-timeout 10 --max-time "$timeout" --retry 2 --retry-delay 1 \
    --retry-max-time "$timeout" "$@"
}
download_asset 15 -fsS "$base/SHA256SUMS" -o "$work/SHA256SUMS"
checksum() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'; else sha256sum "$1" | awk '{print $1}'; fi
}
expected() {
  awk -v file="$1" '$2 == file && $1 ~ /^[[:xdigit:]]+$/ && length($1) == 64 {print tolower($1); exit}' "$work/SHA256SUMS"
}
expected_binary=$(expected "$name")
[ -n "$expected_binary" ] || { say installer.missing.client.checksum >&2; exit 1; }
# A previous persistent authorization may have installed a reusable copy.
cache_root=''
case "$os" in
  macos) if [ -n "${HOME:-}" ]; then cache_root="$HOME/Library/Application Support"; fi ;;
  linux)
    case "${XDG_CONFIG_HOME:-}" in
      /*) cache_root=$XDG_CONFIG_HOME ;;
      '') if [ -n "${HOME:-}" ]; then cache_root="$HOME/.config"; fi ;;
    esac ;;
esac
cached="$cache_root/Agenterm/cli/agenterm-cli-$expected_binary"
if [ -n "$cache_root" ] && [ -f "$cached" ] && [ ! -L "$cached" ] && [ -x "$cached" ] &&
   [ "$(checksum "$cached")" = "$expected_binary" ] &&
   cp -p "$cached" "$binary" && [ "$(checksum "$binary")" = "$expected_binary" ]; then
  say installer.cached.client.verified.skipping.download
else
  if [ -e "$binary" ]; then rm -f "$binary"; fi
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
    download_asset 30 -fsS "$base/$asset.sha256" -o "$work/archive.sha256"
    expected_asset=$(awk 'NR == 1 && $1 ~ /^[[:xdigit:]]+$/ && length($1) == 64 { print tolower($1) }' "$work/archive.sha256")
  fi
  [ -n "$expected_asset" ] || { say installer.missing.archive.checksum >&2; exit 1; }
  if [ -t 2 ]; then
    download_asset 180 -fS --progress-bar "$base/$asset" -o "$work/$asset"
  else
    download_asset 180 -fsS "$base/$asset" -o "$work/$asset"
  fi
  say installer.download.complete.verifying.and.extracting
  [ "$(checksum "$work/$asset")" = "$expected_asset" ] || { say installer.archive.checksum.mismatch >&2; exit 1; }
  if [ "$archive_format" != raw ]; then tar -xf "$work/$asset" -C "$work" "$name"; fi
  [ -f "$work/$name" ] && [ ! -L "$work/$name" ] || { say installer.invalid.client.archive >&2; exit 1; }
  [ "$(checksum "$work/$name")" = "$expected_binary" ] || { say installer.client.checksum.mismatch >&2; exit 1; }
  chmod 700 "$work/$name"
  say installer.client.verified
fi
if ! client_version=$(AGENTERM_SKIP_LINK_REGISTRATION=1 "$binary" -version) ||
   ! printf '%s\n' "$client_version" | awk -F. 'NR == 1 && /^[0-9]+\.[0-9]+\.[0-9]+$/ && ($1 > 0 || $2 > 20 || ($2 == 20 && $3 >= 16)) { supported = 1 } END { exit !(NR == 1 && supported) }'; then
  say installer.client.authorization.version >&2
  exit 1
fi
run_client() {
  TMPDIR="$temp_root" "$binary" -token "$token"
}
if [ ! -t 0 ] && [ -t 1 ] && ( : </dev/tty ) 2>/dev/null; then
  run_client </dev/tty
else
  run_client
fi
