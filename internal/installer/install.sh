#!/bin/sh
set -eu
version='@RELEASE_TAG@'
invitation=${1:?missing invitation}
case "$(uname -s)" in Darwin) os=macos;; Linux) os=linux;; *) echo 'Unsupported system' >&2; exit 1;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64;; x86_64|amd64) arch=amd64;; *) echo 'Unsupported architecture' >&2; exit 1;; esac
base="https://github.com/Heming9/agenterm/releases/download/$version"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
asset="agenterm-cli-$os-$arch.tar.gz"
curl -fsSL "$base/SHA256SUMS" -o "$work/SHA256SUMS"
curl -fsSL "$base/$asset" -o "$work/$asset"
expected=$(awk -v file="$asset" '$2 == file {print $1}' "$work/SHA256SUMS")
[ -n "$expected" ] || { echo 'Missing checksum' >&2; exit 1; }
if command -v shasum >/dev/null 2>&1; then actual=$(shasum -a 256 "$work/$asset" | awk '{print $1}'); else actual=$(sha256sum "$work/$asset" | awk '{print $1}'); fi
[ "$actual" = "$expected" ] || { echo 'Checksum mismatch' >&2; exit 1; }
tar -xzf "$work/$asset" -C "$work"
binary="$work/agenterm-cli-$os-$arch"
chmod +x "$binary"
"$binary" -invite "$invitation"
