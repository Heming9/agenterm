package service

import (
	"agenterm/internal/protocol"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Exercise real capability probes without depending on the host's tar features.
func installTarShim(t *testing.T, directory string) {
	t.Helper()
	realTar, err := exec.LookPath("tar")
	if err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "${AGENTERM_TEST_TAR_MODE:-}" in
 raw) exit 2 ;;
 gzip) for arg do case "$arg" in *.tar.xz) exit 2 ;; esac; done ;;
esac
exec '` + strings.ReplaceAll(realTar, "'", "'\"'\"'") + `' "$@"
`
	if err := os.WriteFile(filepath.Join(directory, "tar"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestShellInvitationDelegatesAuthorizationToClient(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	prefix := protocol.PathPrefix
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{assets, bin} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	name := "agenterm-client-cli-linux-amd64"
	client := []byte("#!/bin/sh\nif [ \"$1\" = -version ]; then printf '0.20.17\\n'; exit 0; fi\nprintf 'arg=%s token=%s\\n' \"$1\" \"$2\"\nif [ -t 0 ]; then printf 'client-input: '; read -r answer; printf 'received:%s\\n' \"$answer\"; fi\n")
	archive := name + ".tar.xz"
	for filename, data := range installerFixture(t, client) {
		write(filepath.Join(assets, filename), data, 0o600)
	}
	write(filepath.Join(bin, "uname"), []byte("#!/bin/sh\ncase $1 in -s) echo Linux;; -m) echo x86_64;; esac\n"), 0o755)
	installTarShim(t, bin)
	log := filepath.Join(root, "downloads")
	state := &runtimeState{}
	var blockWebSocket atomic.Bool
	var mux http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		fmt.Fprintln(f, r.URL.Path)
		f.Close()
		if blockWebSocket.Load() && strings.TrimPrefix(r.URL.Path, prefix) == "/client" {
			http.Error(w, "WebSocket upgrades blocked by proxy", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer ts.Close()
	target, _ := url.Parse(ts.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix+"/") {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer frontend.Close()
	publicURL := frontend.URL
	state.clientURL = "ws" + strings.TrimPrefix(publicURL, "http") + prefix + "/client"
	mux = state.routes(installerTestTag, os.DirFS(assets))
	issued, err := state.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	decodedToken, err := protocol.Decode(issued["client_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf("s=$(curl -fsSL %s)&&sh -c \"$s\"", shQuote(publicURL+prefix+"/c/"+decodedToken.Secret+".sh"))
	cache := filepath.Join(root, "cache")
	run := func() string {
		t.Helper()
		cmd := exec.Command("sh", "-c", command)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "XDG_CONFIG_HOME="+cache)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "arg=-token token="+issued["client_token"].(string)) {
			t.Fatalf("unexpected client output: %s", out)
		}
		output := string(out)
		if strings.Contains(output, "选择授权方式") || strings.Contains(output, "-connection-mode") {
			t.Fatalf("installer must leave authorization to the CLI: %s", output)
		}
		return output
	}
	if output := run(); !strings.Contains(output, "正在下载客户端") {
		t.Fatalf("first run: %s", output)
	}
	cached := filepath.Join(cache, "Agenterm", "cli", fmt.Sprintf("agenterm-cli-%x", sha256.Sum256(client)))
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("bootstrap should not create a persistent install: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cached), 0o700); err != nil {
		t.Fatal(err)
	}
	// Simulate the reusable copy created by the client after persistent approval.
	write(cached, client, 0o700)
	if output := run(); !strings.Contains(output, "本地客户端校验通过，跳过下载") {
		t.Fatalf("second run: %s", output)
	}
	logContent, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(logContent), archive+"\n"); count != 1 {
		t.Fatalf("archive downloaded %d times", count)
	}
	write(cached, []byte("corrupted"), 0o700)
	if output := run(); !strings.Contains(output, "正在下载客户端") {
		t.Fatalf("corrupted cache: %s", output)
	}
	logContent, err = os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(logContent), archive+"\n"); count != 2 {
		t.Fatalf("archive downloaded %d times after corruption", count)
	}
	for _, choice := range []struct{ mode, asset string }{{"gzip", name + ".tar.gz"}, {"raw", name}} {
		t.Setenv("AGENTERM_TEST_TAR_MODE", choice.mode)
		write(cached, []byte("corrupted"), 0700)
		before, _ := os.ReadFile(log)
		if output := run(); !strings.Contains(output, "正在下载客户端") {
			t.Fatalf("%s fallback failed: %s", choice.mode, output)
		}
		after, _ := os.ReadFile(log)
		if !strings.Contains(string(after[len(before):]), "/"+choice.asset+"\n") {
			t.Fatalf("%s downloaded wrong format: %s", choice.mode, after[len(before):])
		}
	}
	t.Setenv("AGENTERM_TEST_TAR_MODE", "")
	// A bad download must never be run or replace the existing cache.
	write(cached, []byte("corrupted"), 0o700)
	write(filepath.Join(assets, archive), []byte("corrupted archive"), 0o600)
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "XDG_CONFIG_HOME="+cache)
	cmd.Stdin = strings.NewReader("2\n")
	out, err := cmd.CombinedOutput()
	if err == nil || strings.Contains(string(out), "arg=-token") {
		t.Fatalf("bad archive was not rejected: err=%v output=%s", err, out)
	}
	// The generated pipeline must read authorization from the terminal, not
	// from the pipe carrying the installer, and pass terminal input onward.
	write(filepath.Join(assets, archive), installerFixture(t, client)[archive], 0o600)
	pipeline, _ := installCommands(issued["client_token"].(string), "")
	wantPipeline := "curl -fL " + strings.TrimPrefix(publicURL, "http://") + prefix + "/c/" + decodedToken.Secret + ".sh|sh"
	if pipeline != wantPipeline {
		t.Fatalf("pipeline = %q, want %q", pipeline, wantPipeline)
	}
	for _, tc := range []struct{ input, want string }{{"1\r", "received:1"}, {"2\r", "received:2"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, "sh", "-c", pipeline)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "XDG_CONFIG_HOME="+filepath.Join(root, "pty-cache"), "TERM=xterm")
		terminal, err := pty.Start(cmd)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		done := make(chan string, 1)
		go func() {
			var output bytes.Buffer
			buffer := make([]byte, 1024)
			sent := false
			for {
				n, err := terminal.Read(buffer)
				output.Write(buffer[:n])
				if !sent && strings.Contains(output.String(), "client-input: ") {
					sent = true
					_, _ = terminal.Write([]byte(tc.input))
				}
				if err != nil {
					done <- output.String()
					return
				}
			}
		}()
		waitErr := cmd.Wait()
		output := <-done
		terminal.Close()
		cancel()
		if waitErr != nil || !strings.Contains(output, tc.want) {
			t.Fatalf("pipeline input %q: %v\n%s", tc.input, waitErr, output)
		}
	}
	// An expired token is rejected before an installer can be downloaded.
	write(cached, client, 0o700)
	state.mu.Lock()
	state.clientOwners[state.tokens[issued["client_token"].(string)]].expires = time.Now().Add(-time.Second)
	state.cleanupTokensLocked(time.Now())
	state.mu.Unlock()
	cmd = exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "XDG_CONFIG_HOME="+cache)
	cmd.Stdin = strings.NewReader("2\n")
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "401") || strings.Contains(string(out), "arg=-token") {
		t.Fatalf("expired token should reject installer: %v %s", err, out)
	}
	// Connection reachability is also decided by the client.
	blockWebSocket.Store(true)
}
