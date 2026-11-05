package service

import (
	"agenterm/internal/protocol"
	"context"
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
	name := "agenterm-client-cli-windows-" + runtime.GOARCH + ".exe"
	source := filepath.Join(root, "main.go")
	if err := os.WriteFile(source, []byte(`package main
import ("fmt"; "os"; "strconv")
func main() { args := append(os.Args[1:], "", "", ""); fmt.Printf("arg=%s invitation=%s consent=%s lang=%s\n", args[0], args[1], args[2], os.Getenv("AGENTERM_LANG")); code, _ := strconv.Atoi(os.Getenv("AGENTERM_TEST_CLIENT_EXIT")); os.Exit(code) }
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
	archive := strings.TrimSuffix(name, ".exe") + ".tar.xz"
	for filename, data := range installerFixture(t, client) {
		if err := os.WriteFile(filepath.Join(root, filename), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	state := &runtimeState{}
	var archiveRequests, manifestRequests, probeRequests, zipRequests, rawRequests atomic.Int32
	var mux http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == protocol.ClientPath {
			probeRequests.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/"+archive) {
			archiveRequests.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/"+strings.TrimSuffix(name, ".exe")+".zip") {
			zipRequests.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/"+name) {
			rawRequests.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
			manifestRequests.Add(1)
		}
		mux.ServeHTTP(w, r)
	}))
	defer ts.Close()
	state.clientURL = "ws" + strings.TrimPrefix(ts.URL, "http") + protocol.ClientPath
	mux = state.routes(installerTestTag, os.DirFS(root))
	issued, err := state.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	value := issued["invitation"].(string)
	_, command := installCommands(issued["client_token"].(string))
	forcedFormat := ""
	customPrefix := ""
	runWithInput := func(input string) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		prefix := ""
		if forcedFormat != "" {
			hidden := `$Name -eq 'tar.exe'`
			if forcedFormat == "raw" {
				hidden += ` -or $Name -eq 'Expand-Archive'`
			}
			prefix = `function Get-Command { [CmdletBinding()] param([string]$Name); if (` + hidden + `) { return }; Microsoft.PowerShell.Core\Get-Command @PSBoundParameters }; `
		}
		// Execute the returned launcher's inner command in this PowerShell.
		// Starting another process would discard the fallback command overrides.
		script, ok := strings.CutPrefix(command, `powershell -NoP -c "`)
		if !ok || !strings.HasSuffix(script, `"`) {
			t.Fatalf("unexpected PowerShell launcher: %s", command)
		}
		script = strings.TrimSuffix(script, `"`)
		cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new(); "+customPrefix+prefix+script+`; Write-Output ("restored-lang=" + $env:AGENTERM_LANG)`)
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
		if err != nil || !strings.Contains(out, "已取消连接。") || strings.Contains(out, "连接进度") {
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
		if err != nil || !strings.Contains(out, message) || !strings.Contains(out, "arg=-token invitation="+issued["client_token"].(string)) {
			t.Fatalf("run %d: %v\n%s", i, err, out)
		}
		if strings.Count(out, "是否允许连接？[y/N]") != 1 || !strings.Contains(out, "请确保你信任对方") {
			t.Fatalf("missing or duplicate consent prompt: %s", out)
		}
		previous := -1
		for _, stage := range []string{"⚠️ 远程控制授权", "是否允许连接？[y/N]", "连接进度", "[1/4]", "[2/4]", "arg=-token"} {
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
	customPrefix = `[Threading.Thread]::CurrentThread.CurrentUICulture = 'en-US'; [Threading.Thread]::CurrentThread.CurrentCulture = 'zh-CN'; $env:AGENTERM_LANG = 'auto'; `
	if out, err := run(); err != nil || !strings.Contains(out, "Allow connection?") || !strings.Contains(out, "lang=en") || !strings.Contains(out, "restored-lang=auto") {
		t.Fatalf("display language differs from regional format: %v\n%s", err, out)
	}
	customPrefix = `[Threading.Thread]::CurrentThread.CurrentUICulture = 'en-US'; $env:AGENTERM_LANG = 'zh-CN'; `
	if out, err := run(); err != nil || !strings.Contains(out, "是否允许连接？") || !strings.Contains(out, "lang=zh-CN") || !strings.Contains(out, "restored-lang=zh-CN") {
		t.Fatalf("explicit language override: %v\n%s", err, out)
	}
	customPrefix = ""
	customPrefix = `$env:AGENTERM_TEST_CLIENT_EXIT = '7'; `
	if out, err := run(); err == nil {
		t.Fatalf("client failure returned success: %s", out)
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 7 {
		t.Fatalf("client exit code not propagated: %v\n%s", err, out)
	}
	customPrefix = ""
	cached := filepath.Join(root, "local", "Agenterm", "cache", installerTestTag, "windows-"+runtime.GOARCH, name)
	if err := os.WriteFile(cached, []byte("corrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); err != nil || !strings.Contains(out, "正在下载客户端") || archiveRequests.Load() != 2 {
		t.Fatalf("corrupt cache: %v\n%s", err, out)
	}
	for _, choice := range []struct {
		format string
		count  *atomic.Int32
	}{{"zip", &zipRequests}, {"raw", &rawRequests}} {
		forcedFormat = choice.format
		before := choice.count.Load()
		if err := os.WriteFile(cached, []byte("corrupted"), 0600); err != nil {
			t.Fatal(err)
		}
		if out, err := run(); err != nil || !strings.Contains(out, "arg=-token invitation="+issued["client_token"].(string)) || choice.count.Load() != before+1 {
			t.Fatalf("%s fallback failed: %v %s", choice.format, err, out)
		}
	}
	forcedFormat = ""
	_, _ = protocol.Decode(value)
	state.mu.Lock()
	state.clientOwners[state.tokens[issued["client_token"].(string)]].expires = time.Now().Add(-time.Second)
	state.mu.Unlock()
	state.mu.Lock()
	state.cleanupTokensLocked(time.Now())
	state.mu.Unlock()
	if out, err := run(); err != nil || !strings.Contains(out, "arg=-token= invitation=") {
		t.Fatalf("expired invitation should launch client: %v\n%s", err, out)
	}
}
