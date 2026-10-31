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
import ("bufio"; "fmt"; "os"; "strings")
func main() { answer, _ := bufio.NewReader(os.Stdin).ReadString('\n'); fmt.Printf("consent=%s arg=%s invitation=%s\n", strings.TrimSpace(answer), os.Args[1], os.Args[2]) }
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
	var archiveRequests, manifestRequests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/client", state.connect)
	mux.Handle("/c/", state.installWrapper("v0.1.8"))
	mux.Handle("/downloads/", downloadHandler("v0.1.8", os.DirFS(root)))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/"+archive) {
			archiveRequests.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
			manifestRequests.Add(1)
		}
		mux.ServeHTTP(w, r)
	}))
	defer ts.Close()
	state.clientURL = "ws" + strings.TrimPrefix(ts.URL, "http") + "/client"
	issued, err := state.createClient()
	if err != nil {
		t.Fatal(err)
	}
	value := issued["invitation"].(string)
	_, command := installCommands(value)
	run := func() (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new(); "+command)
		cmd.Env = append(os.Environ(), "LOCALAPPDATA="+filepath.Join(root, "local"))
		cmd.Stdin = strings.NewReader("y\n")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for i, message := range []string{"正在下载客户端", "本地客户端校验通过，跳过下载"} {
		out, err := run()
		if err != nil || !strings.Contains(out, message) || !strings.Contains(out, "consent=y arg=-invite invitation="+value) {
			t.Fatalf("run %d: %v\n%s", i, err, out)
		}
	}
	if archiveRequests.Load() != 1 {
		t.Fatalf("cache not reused: downloads=%d", archiveRequests.Load())
	}
	cached := filepath.Join(root, "local", "Agenterm", "cache", "v0.1.8", "windows-"+runtime.GOARCH, name)
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
	if out, err := run(); err == nil || !strings.Contains(out, "401") {
		t.Fatalf("expired invitation: %v\n%s", err, out)
	}
	if manifestRequests.Load() != before || archiveRequests.Load() != 2 {
		t.Fatal("installer downloaded resources after a failed probe")
	}
}
