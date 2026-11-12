#!/usr/bin/env bash
# Install the static linker, falling back to a signed Ubuntu mirror when the
# runner's normal archive is unreachable. APT still verifies repository keys.
set -euo pipefail

if command -v musl-gcc >/dev/null 2>&1; then
  exit 0
fi

apt_root=${AGENTERM_APT_ETC:-/etc/apt}
if [ "$(dpkg --print-architecture)" = arm64 ]; then
  host=ports.ubuntu.com
  fallback=https://mirrors.ustc.edu.cn/ubuntu-ports
else
  host=archive.ubuntu.com
  fallback=https://mirrors.ustc.edu.cn/ubuntu
fi

use_fallback() {
  local changed=0 source
  if [ -f "$apt_root/apt-mirrors.txt" ]; then
    echo "$fallback" | sudo tee "$apt_root/apt-mirrors.txt" >/dev/null
    changed=1
  fi
  for source in "$apt_root/sources.list" "$apt_root/sources.list.d/ubuntu.sources"; do
    if [ -f "$source" ]; then
      sudo sed -i -e "s|http://ports.ubuntu.com/ubuntu-ports|$fallback|g" \
        -e "s|https://ports.ubuntu.com/ubuntu-ports|$fallback|g" \
        -e "s|http://azure.archive.ubuntu.com/ubuntu|$fallback|g" \
        -e "s|https://azure.archive.ubuntu.com/ubuntu|$fallback|g" \
        -e "s|http://archive.ubuntu.com/ubuntu|$fallback|g" \
        -e "s|https://archive.ubuntu.com/ubuntu|$fallback|g" "$source"
      changed=1
    fi
  done
  if [ "$changed" -eq 0 ]; then
    echo 'No Ubuntu APT sources found for fallback' >&2
    return 1
  fi
}

install_tools() {
  timeout 90s sudo apt-get -o Acquire::Retries=0 -o Acquire::http::Timeout=10 -o Acquire::https::Timeout=10 update &&
    timeout 120s sudo apt-get -o Acquire::Retries=0 -o Acquire::http::Timeout=10 -o Acquire::https::Timeout=10 install -y --no-install-recommends musl-tools
}

if ! timeout 10s getent ahosts "$host" >/dev/null; then
  use_fallback
  install_tools
elif ! install_tools; then
  echo 'Ubuntu archive unavailable; retrying with the signed fallback mirror' >&2
  use_fallback
  install_tools
fi
