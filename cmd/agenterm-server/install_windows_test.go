package main

import (
	"agenterm/internal/protocol"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPowerShellInvitationInstaller(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows PowerShell and a Windows executable")
	}
	root := t.TempDir()
	name := "agenterm-cli-windows-" + runtime.GOARCH + ".exe"
	source := filepath.Join(root, "main.go")
	if err := os.WriteFile(source, []byte(`package main
import ("fmt"; "os")
func main() { fmt.Printf("arg=%s invitation=%s consent=%s\n", os.Args[1], os.Args[2], os.Args[3]) }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(root, name), source)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	client, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	zw := zip.NewWriter(&data)
	f, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(client); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	archive := strings.TrimSuffix(name, ".exe") + ".zip"
	if err := os.WriteFile(filepath.Join(root, archive), data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf("%x  %s\n%x  %s\n", sha256.Sum256(data.Bytes()), archive, sha256.Sum256(client), name)
	if err := os.WriteFile(filepath.Join(root, "SHA256SUMS"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	state := &server{}
	var archiveRequests, manifestRequests, probeRequests atomic.Int32
	mux := state.routes(installerTestTag, os.DirFS(root))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == protocol.ClientPath {
			probeRequests.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/"+archive) {
			archiveRequests.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
			manifestRequests.Add(1)
		}
		mux.ServeHTTP(w, r)
	}))
	defer ts.Close()
	state.clientURL = "ws" + strings.TrimPrefix(ts.URL, "http") + protocol.ClientPath
	issued, err := state.createClient()
	if err != nil {
		t.Fatal(err)
	}
	value := issued["invitation"].(string)
	_, command := installCommands(value)
	runWithInput := func(input string) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new(); "+command)
		// CI runs under PowerShell 7. Let Windows PowerShell construct its own
		// module path instead of trying to load the inherited PowerShell 7 modules.
		for _, value := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(value), "PSMODULEPATH=") {
				cmd.Env = append(cmd.Env, value)
			}
		}
		cmd.Env = append(cmd.Env, "LOCALAPPDATA="+filepath.Join(root, "local"))
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, answer := range []string{"n\n", "\n", "", "yes\n"} {
		out, err := runWithInput(answer)
		if err != nil || !strings.Contains(out, "已取消连接。") || strings.Contains(out, "[2/5]") {
			t.Fatalf("cancel with %q: %v\n%s", answer, err, out)
		}
		if probeRequests.Load() != 0 || manifestRequests.Load() != 0 || archiveRequests.Load() != 0 {
			t.Fatal("installer probed or downloaded before consent")
		}
		if _, err := os.Stat(filepath.Join(root, "local")); !os.IsNotExist(err) {
			t.Fatalf("cache created before consent: %v", err)
		}
	}
	run := func() (string, error) { return runWithInput("y\n") }
	for i, message := range []string{"正在下载客户端", "本地客户端校验通过，跳过下载"} {
		out, err := run()
		if err != nil || !strings.Contains(out, message) || !strings.Contains(out, "arg=-invite invitation="+value+" consent=-consent-confirmed") {
			t.Fatalf("run %d: %v\n%s", i, err, out)
		}
		if strings.Count(out, "是否允许连接？[y/N]") != 1 || !strings.Contains(out, "请确保你信任对方") {
			t.Fatalf("missing or duplicate consent prompt: %s", out)
		}
		previous := -1
		for _, stage := range []string{"[1/5]", "[2/5]", "[3/5]", "arg=-invite"} {
			index := strings.Index(out, stage)
			if index <= previous {
				t.Fatalf("unexpected stage order: %s", out)
			}
			previous = index
		}
	}
	if archiveRequests.Load() != 1 {
		t.Fatalf("cache not reused: downloads=%d", archiveRequests.Load())
	}
	cached := filepath.Join(root, "local", "Agenterm", "cache", installerTestTag, "windows-"+runtime.GOARCH, name)
	if err := os.WriteFile(cached, []byte("corrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); err != nil || !strings.Contains(out, "正在下载客户端") || archiveRequests.Load() != 2 {
		t.Fatalf("corrupt cache: %v\n%s", err, out)
	}
	decoded, _ := protocol.Decode(value)
	state.mu.Lock()
	entry := state.invitations[decoded.Secret]
	entry.expires = time.Now().Add(-time.Second)
	state.invitations[decoded.Secret] = entry
	state.mu.Unlock()
	before := manifestRequests.Load()
	if out, err := run(); err == nil || !strings.Contains(out, "invitation invalid, expired, or already used") {
		t.Fatalf("expired invitation: %v\n%s", err, out)
	}
	if manifestRequests.Load() != before || archiveRequests.Load() != 2 {
		t.Fatal("installer downloaded resources after a failed probe")
	}
	// A script from an incompatible release must fail before any downloads.
	incompatible := &server{}
	var unexpectedDownloads atomic.Int32
	incompatibleRoutes := incompatible.routes("v9.9.9", nil)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/downloads/") {
			unexpectedDownloads.Add(1)
		}
		incompatibleRoutes.ServeHTTP(w, r)
	}))
	defer other.Close()
	incompatible.clientURL = "ws" + strings.TrimPrefix(other.URL, "http") + protocol.ClientPath
	invitation, err := incompatible.createClient()
	if err != nil {
		t.Fatal(err)
	}
	_, command = installCommands(invitation["invitation"].(string))
	if out, err := run(); err == nil || !strings.Contains(out, "版本不兼容") {
		t.Fatalf("incompatible installer: %v\n%s", err, out)
	}
	incompatible.mu.Lock()
	defer incompatible.mu.Unlock()
	if unexpectedDownloads.Load() != 0 || len(incompatible.invitations) != 1 || len(incompatible.clients) != 0 {
		t.Fatal("incompatible installer downloaded files, redeemed an invitation, or registered a client")
	}
}
