//go:build !windows

package installer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestShellInstallerRetriesTimedOutDownload(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is required to exercise installer retries")
	}
	const name = "agenterm-client-cli-linux-amd64"
	payload := []byte("#!/bin/sh\nprintf 'client:%s\\n' \"$*\"\n")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/downloads/v1/SHA256SUMS":
			fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(payload), name)
		case "/downloads/v1/" + name:
			if attempts.Add(1) == 1 {
				<-r.Context().Done()
				return
			}
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	script, err := Script("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// Use an isolated cache and the raw binary path. Keep real curl retry
	// behavior while shortening its per-attempt timeout for this test.
	for name, data := range map[string][]byte{
		"install.sh": script,
		"uname":      []byte("#!/bin/sh\ncase $1 in -s) echo Linux;; -m) echo x86_64;; esac\n"),
		"tar":        []byte("#!/bin/sh\nexit 1\n"),
		"curl":       []byte("#!/bin/sh\nexec \"$AGENTERM_TEST_CURL\" \"$@\" --max-time 0.5\n"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", filepath.Join(dir, "install.sh"), "test-invitation", server.URL+"/downloads/v1")
	cmd.Env = append(os.Environ(), "AGENTERM_LANG=en", "AGENTERM_TEST_CURL="+curl, "PATH="+dir+":"+os.Getenv("PATH"),
		"XDG_CACHE_HOME="+dir, "NO_PROXY=127.0.0.1", "no_proxy=127.0.0.1")
	cmd.Stdin = strings.NewReader("1\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer failed to resume after timeout: %v\n%s", err, output)
	}
	const invocation = "client:-invite test-invitation -consent-confirmed -connection-mode=temporary"
	if got := attempts.Load(); got != 2 || strings.Count(string(output), invocation) != 1 {
		t.Fatalf("expected two download attempts and one client launch, got %d attempts\n%s", got, output)
	}
}
