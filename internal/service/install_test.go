package service

import (
	"agenterm/internal/protocol"
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

func TestShellInvitationConfirmsBeforePreparingClient(t *testing.T) {
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
	client := []byte("#!/bin/sh\nprintf 'arg=%s invitation=%s consent=%s\\n' \"$1\" \"$2\" \"$3\"\n")
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
	invitation := issued["invitation"].(string)
	command, _ := installCommands(issued["client_token"].(string))
	cache := filepath.Join(root, "cache")
	// Cancellation must happen before probing, creating a cache, or downloading.
	for _, answer := range []string{"n\n", "\n", "", "yes\n"} {
		before, _ := os.ReadFile(log)
		cmd := exec.Command("sh", "-c", command)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "XDG_CACHE_HOME="+cache)
		cmd.Stdin = strings.NewReader(answer)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "已取消连接。") || strings.Contains(string(out), "连接进度") {
			t.Fatalf("cancel with %q: %v\n%s", answer, err, out)
		}
		after, _ := os.ReadFile(log)
		requests := string(after[len(before):])
		if strings.Contains(requests, "/client") || strings.Contains(requests, "/downloads/") {
			t.Fatalf("requests after cancellation: %s", requests)
		}
		if _, err := os.Stat(cache); !os.IsNotExist(err) {
			t.Fatalf("cache created before consent: %v", err)
		}
	}
	run := func() string {
		t.Helper()
		cmd := exec.Command("sh", "-c", command)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "XDG_CACHE_HOME="+cache)
		cmd.Stdin = strings.NewReader("2\n")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "arg=-token invitation="+issued["client_token"].(string)) {
			t.Fatalf("unexpected client output: %s", out)
		}
		if !strings.Contains(string(out), "consent=-connection-mode=persistent") {
			t.Fatalf("persistent choice was not passed to client: %s", out)
		}
		output := string(out)
		if strings.Count(output, "选择授权方式（↑/↓/←/→，回车确认）：") != 1 || !strings.Contains(output, "请确保你信任对方") {
			t.Fatalf("missing or duplicate consent prompt: %s", output)
		}
		previous := -1
		for _, stage := range []string{"⚠️ 远程控制授权", "选择授权方式（↑/↓/←/→，回车确认）：", "连接进度", "[2/4]", "arg=-token"} {
			index := strings.Index(output, stage)
			if index <= previous {
				t.Fatalf("unexpected stage order: %s", output)
			}
			previous = index
		}
		return output
	}
	if output := run(); !strings.Contains(output, "正在下载客户端") {
		t.Fatalf("first run: %s", output)
	}
	if output := run(); !strings.Contains(output, "本地客户端校验通过，跳过下载") {
		t.Fatalf("second run: %s", output)
	}
	cmdTemporary := exec.Command("sh", "-c", command)
	cmdTemporary.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "XDG_CACHE_HOME="+cache)
	cmdTemporary.Stdin = strings.NewReader("1\n")
	if out, err := cmdTemporary.CombinedOutput(); err != nil || !strings.Contains(string(out), "arg=-token invitation="+issued["client_token"].(string)+" consent=-connection-mode=temporary") {
		t.Fatalf("temporary choice was not passed to client: %v\n%s", err, out)
	}
	logContent, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(logContent), archive+"\n"); count != 1 {
		t.Fatalf("archive downloaded %d times", count)
	}
	cached := filepath.Join(cache, "agenterm", installerTestTag, "linux-amd64", name)
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
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "XDG_CACHE_HOME="+cache)
	cmd.Stdin = strings.NewReader("2\n")
	out, err := cmd.CombinedOutput()
	if err == nil || strings.Contains(string(out), "arg=-token") {
		t.Fatalf("bad archive was not rejected: err=%v output=%s", err, out)
	}
	// An expired token launches the client without credentials.
	write(cached, client, 0o700)
	_, _ = protocol.Decode(invitation)
	state.mu.Lock()
	state.clientOwners[state.tokens[issued["client_token"].(string)]].expires = time.Now().Add(-time.Second)
	state.cleanupTokensLocked(time.Now())
	state.mu.Unlock()
	cmd = exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "XDG_CACHE_HOME="+cache)
	cmd.Stdin = strings.NewReader("2\n")
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "arg=-token invitation= consent=") {
		t.Fatalf("expired launch: %v %s", err, out)
	}
	// Connection reachability is also decided by the client.
	blockWebSocket.Store(true)
}
