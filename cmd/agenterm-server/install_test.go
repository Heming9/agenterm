package main

import (
	"agenterm/internal/protocol"
	"archive/tar"
	"bytes"
	"compress/gzip"
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
)

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
	name := "agenterm-cli-linux-amd64"
	client := []byte("#!/bin/sh\nprintf 'arg=%s invitation=%s consent=%s\\n' \"$1\" \"$2\" \"$3\"\n")
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
	log := filepath.Join(root, "downloads")
	state := &server{}
	var blockWebSocket atomic.Bool
	mux := state.routes(installerTestTag, os.DirFS(assets))
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
	publicURL := ts.URL
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
	publicURL = frontend.URL
	state.clientURL = "ws" + strings.TrimPrefix(publicURL, "http") + prefix + "/client"
	issued, err := state.createClient()
	if err != nil {
		t.Fatal(err)
	}
	invitation := issued["invitation"].(string)
	command, _ := installCommands(invitation)
	cache := filepath.Join(root, "cache")
	// Cancellation must happen before probing, creating a cache, or downloading.
	for _, answer := range []string{"n\n", "\n", "", "yes\n"} {
		before, _ := os.ReadFile(log)
		cmd := exec.Command("sh", "-c", command)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "XDG_CACHE_HOME="+cache)
		cmd.Stdin = strings.NewReader(answer)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "已取消连接。") || strings.Contains(string(out), "[2/5]") {
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
		cmd.Stdin = strings.NewReader("y\n")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "arg=-invite invitation="+invitation+" consent=-consent-confirmed") {
			t.Fatalf("unexpected client output: %s", out)
		}
		output := string(out)
		if strings.Count(output, "是否允许连接？[y/N]") != 1 || !strings.Contains(output, "请确保你信任对方") {
			t.Fatalf("missing or duplicate consent prompt: %s", output)
		}
		previous := -1
		for _, stage := range []string{"[1/5]", "[2/5]", "[3/5]", "arg=-invite"} {
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
	// A bad download must never be run or replace the existing cache.
	write(cached, []byte("corrupted"), 0o700)
	write(filepath.Join(assets, archive), []byte("corrupted archive"), 0o600)
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "XDG_CACHE_HOME="+cache)
	cmd.Stdin = strings.NewReader("y\n")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "Archive checksum mismatch") || strings.Contains(string(out), "arg=-invite") {
		t.Fatalf("bad archive was not rejected: err=%v output=%s", err, out)
	}
	// Invalid/expired invitations must fail before fetching the manifest or archive,
	// even when the local cache is missing or damaged.
	before, _ := os.ReadFile(log)
	decoded, _ := protocol.Decode(invitation)
	state.mu.Lock()
	entry := state.invitations[decoded.Secret]
	entry.expires = time.Now().Add(-time.Second)
	state.invitations[decoded.Secret] = entry
	state.mu.Unlock()
	cmd = exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "XDG_CACHE_HOME="+cache)
	cmd.Stdin = strings.NewReader("y\n")
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "401") {
		t.Fatalf("expired preflight: err=%v output=%s", err, out)
	}
	after, _ := os.ReadFile(log)
	if strings.Contains(string(after[len(before):]), "SHA256SUMS") || strings.Contains(string(after[len(before):]), archive) {
		t.Fatalf("download continued after failed probe: %s", after[len(before):])
	}
	// Being able to fetch the script over HTTP does not imply WebSocket support.
	state.mu.Lock()
	entry.expires = time.Now().Add(time.Minute)
	state.invitations[decoded.Secret] = entry
	state.mu.Unlock()
	blockWebSocket.Store(true)
	before = after
	cmd = exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "XDG_CACHE_HOME="+cache)
	cmd.Stdin = strings.NewReader("y\n")
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "WebSocket connectivity check failed (HTTP 403)") {
		t.Fatalf("blocked WebSocket: err=%v output=%s", err, out)
	}
	after, _ = os.ReadFile(log)
	if strings.Contains(string(after[len(before):]), "SHA256SUMS") || strings.Contains(string(after[len(before):]), archive) {
		t.Fatalf("download continued after blocked WebSocket: %s", after[len(before):])
	}
}

func TestShellInstallerVersionMismatchStopsDownload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	state := &server{}
	var downloads atomic.Int32
	routes := state.routes("v9.9.9", nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/downloads/") {
			downloads.Add(1)
		}
		routes.ServeHTTP(w, r)
	}))
	defer ts.Close()
	state.clientURL = "ws" + strings.TrimPrefix(ts.URL, "http") + protocol.ClientPath
	issued, err := state.createClient()
	if err != nil {
		t.Fatal(err)
	}
	command, _ := installCommands(issued["invitation"].(string))
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "XDG_CACHE_HOME="+t.TempDir())
	cmd.Stdin = strings.NewReader("y\n")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "版本不兼容") || !strings.Contains(string(out), "v9.9.9") {
		t.Fatalf("incompatible installer: %v\n%s", err, out)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if downloads.Load() != 0 || len(state.invitations) != 1 || len(state.clients) != 0 {
		t.Fatal("incompatible installer downloaded files, redeemed the invitation, or registered a client")
	}
}
