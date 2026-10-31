package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestShellInvitationPreservesConsentInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{assets, bin} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	template, err := os.ReadFile(filepath.Join("..", "..", "internal", "installer", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	write := func(path string, data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(assets, "install.sh"), []byte(strings.ReplaceAll(string(template), "@RELEASE_TAG@", "v0.1.8")), 0o600)
	name := "agenterm-cli-linux-amd64"
	client := []byte("#!/bin/sh\nread -r answer\nprintf 'consent=%s arg=%s invitation=%s\\n' \"$answer\" \"$1\" \"$2\"\n")
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tarball := tar.NewWriter(gz)
	if err := tarball.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(client))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarball.Write(client); err != nil {
		t.Fatal(err)
	}
	if err := tarball.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := name + ".tar.gz"
	write(filepath.Join(assets, archive), data.Bytes(), 0o600)
	write(filepath.Join(assets, "SHA256SUMS"), []byte(fmt.Sprintf("%x  %s\n%x  %s\n", sha256.Sum256(data.Bytes()), archive, sha256.Sum256(client), name)), 0o600)
	write(filepath.Join(bin, "uname"), []byte("#!/bin/sh\ncase $1 in -s) echo Linux;; -m) echo x86_64;; esac\n"), 0o755)
	write(filepath.Join(bin, "curl"), []byte(`#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in
    https://github.com/Heming9/agenterm/releases/download/v0.1.8/*) src=${1##*/} ;;
    -o) shift; dst=$1 ;;
  esac
  shift
done
[ -n "${src:-}" ] || exit 1
printf '%s\n' "$src" >> "$TEST_DOWNLOAD_LOG"
if [ -n "${dst:-}" ]; then cp "$TEST_ASSETS/$src" "$dst"; else cat "$TEST_ASSETS/$src"; fi
`), 0o755)
	command, _ := installCommands("v0.1.8", "agenterm:test")
	log := filepath.Join(root, "downloads")
	cache := filepath.Join(root, "cache")
	run := func() string {
		t.Helper()
		cmd := exec.Command("sh", "-c", command)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "XDG_CACHE_HOME="+cache, "TEST_ASSETS="+assets, "TEST_DOWNLOAD_LOG="+log)
		cmd.Stdin = strings.NewReader("y\n")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "consent=y arg=-invite invitation=agenterm:test") {
			t.Fatalf("unexpected client output: %s", out)
		}
		return string(out)
	}
	if output := run(); !strings.Contains(output, "Downloading client.") {
		t.Fatalf("first run: %s", output)
	}
	if output := run(); !strings.Contains(output, "Using verified cached client.") {
		t.Fatalf("second run: %s", output)
	}
	logContent, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(logContent), archive+"\n"); count != 1 {
		t.Fatalf("archive downloaded %d times", count)
	}
	cached := filepath.Join(cache, "agenterm", "v0.1.8", "linux-amd64", name)
	write(cached, []byte("corrupted"), 0o700)
	if output := run(); !strings.Contains(output, "Downloading client.") {
		t.Fatalf("corrupted cache: %s", output)
	}
	logContent, err = os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(logContent), archive+"\n"); count != 2 {
		t.Fatalf("archive downloaded %d times after corruption", count)
	}
	// A bad download must never be run or replace the existing cache.
	write(cached, []byte("corrupted"), 0o700)
	write(filepath.Join(assets, archive), []byte("corrupted archive"), 0o600)
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "XDG_CACHE_HOME="+cache, "TEST_ASSETS="+assets, "TEST_DOWNLOAD_LOG="+log)
	cmd.Stdin = strings.NewReader("y\n")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "Archive checksum mismatch") || strings.Contains(string(out), "consent=y") {
		t.Fatalf("bad archive was not rejected: err=%v output=%s", err, out)
	}
}
