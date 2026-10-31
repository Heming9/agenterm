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
	write(filepath.Join(assets, "SHA256SUMS"), []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(data.Bytes()), archive)), 0o600)
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
if [ -n "${dst:-}" ]; then cp "$TEST_ASSETS/$src" "$dst"; else cat "$TEST_ASSETS/$src"; fi
`), 0o755)
	command, _ := installCommands("v0.1.8", "agenterm:test")
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "TEST_ASSETS="+assets)
	cmd.Stdin = strings.NewReader("y\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "consent=y arg=-invite invitation=agenterm:test") {
		t.Fatalf("unexpected client output: %s", out)
	}
}
