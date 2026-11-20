package service

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

func TestTemporaryAssistanceInvitationPolicy(t *testing.T) {
	h := newMultiHarness(t, 0)
	h.call(t, "admin-test", "create_client", map[string]any{"connection_mode": "persistent"}, true)
	ordinary := h.call(t, "admin-test", "create_client", map[string]any{"connection_mode": ""}, false).(map[string]any)
	token, err := protocol.Decode(deviceTokenFromControl(ordinary["control_token"].(string)))
	if err != nil || token.TemporaryOnly {
		t.Fatalf("empty mode must leave the connection mode open: %v", err)
	}
	issued := h.call(t, "admin-test", "create_client", map[string]any{"connection_mode": "temporary"}, false).(map[string]any)
	token, err = protocol.Decode(deviceTokenFromControl(issued["control_token"].(string)))
	if err != nil || !token.TemporaryOnly {
		t.Fatalf("device token must carry the temporary-only policy: %v", err)
	}
	display := issued["display_text"].(string)
	code := displayShortCode(t, display)
	for _, credential := range []string{code, token.Secret} {
		request := httptest.NewRequest(http.MethodHead, "/c/"+credential, nil)
		response := httptest.NewRecorder()
		h.s.installWrapper(installerTestTag).ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get(protocol.ConnectionModeHeader) != "temporary" {
			t.Fatalf("invitation preflight returned %d without the temporary-only policy", response.Code)
		}
	}
	h.s.mu.Lock()
	redemptions := h.s.shortCodes[code].redemptions
	h.s.mu.Unlock()
	if redemptions != 0 {
		t.Fatal("checking the invitation mode redeemed the short code")
	}
	headers := http.Header{"Authorization": {"Bearer " + token.Secret}, version.Header: {version.Protocol}}
	for _, mode := range []string{"", "persistent", "temporary"} {
		headers.Set(protocol.ConnectionModeHeader, mode)
		conn, response, err := websocket.DefaultDialer.Dial(token.ConnectionURL(), headers)
		if response == nil {
			t.Fatalf("missing handshake response for mode %q: %v", mode, err)
		}
		if mode == "temporary" {
			if err != nil || response.Header.Get(protocol.ConnectionModeHeader) != mode {
				t.Fatalf("temporary handshake failed: %v", err)
			}
			conn.Close()
		} else {
			if conn != nil {
				conn.Close()
			}
			response.Body.Close()
			if err == nil || response.StatusCode != http.StatusForbidden {
				t.Fatalf("mode %q must be rejected: HTTP %d (%v)", mode, response.StatusCode, err)
			}
		}
	}
}

func TestTemporaryAssistanceInstallerCleanup(t *testing.T) {
	t.Run("user-selects-temporary", func(t *testing.T) { testTemporaryAssistanceInstallerCleanup(t, "") })
	t.Run("temporary-only-invitation", func(t *testing.T) { testTemporaryAssistanceInstallerCleanup(t, "temporary") })
}

func testTemporaryAssistanceInstallerCleanup(t *testing.T, mode string) {
	binary, err := rustClientBinary()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	osName := runtime.GOOS
	if osName == "darwin" {
		osName = "macos"
	}
	name := "agenterm-client-cli-" + osName + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	assets := fstest.MapFS{
		name:         {Data: data},
		"SHA256SUMS": {Data: fmt.Appendf(nil, "%x  %s\n", sha256.Sum256(data), name)},
	}
	s := &runtimeState{enableShortCode: true, adminKey: "admin-test"}
	ts := httptest.NewServer(s.routes(installerTestTag, assets))
	defer ts.Close()
	s.clientURL = "ws" + strings.TrimPrefix(ts.URL, "http") + protocol.ClientPath
	h := &multiHarness{s: s, ts: ts}
	issued := h.call(t, "admin-test", "create_client", map[string]any{"connection_mode": mode}, false).(map[string]any)
	control := issued["control_token"].(string)
	token, err := protocol.Decode(deviceTokenFromControl(control))
	if err != nil {
		t.Fatal(err)
	}
	root, home, temp := t.TempDir(), t.TempDir(), t.TempDir()
	_, shellBlock, _ := strings.Cut(issued["display_text"].(string), "macOS / Linux one-click connection command:\n```\n")
	shell, _, foundShell := strings.Cut(shellBlock, "\n```")
	if !foundShell || shell == "" {
		t.Fatal("connection instructions are missing the shell command")
	}
	profile := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runInstaller := func(input string, interactive bool) (*exec.Cmd, *bytes.Buffer) {
		request := ts.URL + protocol.PathPrefix + "/c/" + token.Secret
		cmd := exec.CommandContext(ctx, "sh", "-c", "body=$(curl -fsSL "+shQuote(request+".sh")+") && sh -c \"$body\"")
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
				"[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new(); irm "+psQuote(request+".ps1")+"|iex")
		} else if interactive {
			cmd = exec.CommandContext(ctx, "sh", "-c", shell)
		}
		cmd.Dir = root
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(entry), "PSMODULEPATH=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "HOME="+home, "USERPROFILE="+profile, "APPDATA="+home, "LOCALAPPDATA="+home,
			"XDG_CACHE_HOME="+home, "XDG_CONFIG_HOME="+home, "TMPDIR="+temp, "TEMP="+temp, "TMP="+temp, "AGENTERM_LANG=en",
			"TERM=xterm", "SHELL=/bin/bash", "HISTFILE="+filepath.Join(home, "shell-history"))
		var output bytes.Buffer
		if !interactive {
			cmd.Stdin = strings.NewReader(input)
			cmd.Stdout, cmd.Stderr = &output, &output
		}
		return cmd, &output
	}
	assertEmpty := func(dir string) {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("expected empty directory %s, got %v: %v", dir, entries, err)
		}
	}
	denied, deniedOutput := runInstaller("\n", false)
	if err := denied.Run(); err != nil || !strings.Contains(deniedOutput.String(), "cancelled") {
		t.Fatalf("declining consent: %v\n%s", err, deniedOutput)
	}
	wantChoices := []string{"Choose authorization", "1. Allow authorization (temporary)", "Enter a choice:"}
	if mode == "temporary" {
		wantChoices = append(wantChoices, "2. Deny authorization")
		if strings.Contains(deniedOutput.String(), "Allow authorization (persistent)") {
			t.Fatal("restricted invitation offered persistent approval")
		}
		for _, value := range []string{deviceTokenFromControl(control), ts.URL + protocol.PathPrefix + "/c/" + displayShortCode(t, issued["display_text"].(string))} {
			direct := exec.CommandContext(ctx, binary, "-plain", "-token", value)
			direct.Env = denied.Env
			direct.Stdin = strings.NewReader("2\n")
			if output, err := direct.CombinedOutput(); err != nil || !strings.Contains(string(output), "2. Deny authorization") ||
				strings.Contains(string(output), "Allow authorization (persistent)") {
				t.Fatalf("restricted token/address authorization: %v\n%s", err, output)
			}
			persistent := exec.CommandContext(ctx, binary, "-plain", "-token", value, "-connection-mode", "persistent")
			persistent.Env = denied.Env
			if output, err := persistent.CombinedOutput(); err == nil || !strings.Contains(string(output), "requires a temporary connection") {
				t.Fatalf("forcing persistent mode with a restricted token/address: %v\n%s", err, output)
			}
		}
	} else {
		wantChoices = append(wantChoices, "2. Allow authorization (persistent)", "3. Deny authorization")
	}
	for _, want := range wantChoices {
		if !strings.Contains(deniedOutput.String(), want) {
			t.Errorf("consent prompt missing %q:\n%s", want, deniedOutput)
		}
	}
	assertEmpty(temp)
	assertEmpty(home)
	interactive := runtime.GOOS != "windows"
	cmd, output := runInstaller("1\n", interactive)
	var terminal *os.File
	var readDone chan struct{}
	authorizationReady := make(chan struct{})
	if interactive {
		terminal, err = pty.StartWithSize(cmd, &pty.Winsize{Cols: 140, Rows: 24})
		if err != nil {
			t.Fatal(err)
		}
		readDone = make(chan struct{})
		go func() {
			defer close(readDone)
			buffer := make([]byte, 4096)
			ready := false
			for {
				count, err := terminal.Read(buffer)
				output.Write(buffer[:count])
				if !ready && bytes.Contains(output.Bytes(), []byte("Deny authorization")) {
					close(authorizationReady)
					ready = true
				}
				if err != nil {
					return
				}
			}
		}()
	} else if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		if terminal != nil {
			terminal.Close()
			<-readDone
		}
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			return
		}
		if t.Failed() {
			t.Logf("installer output: %s", output)
		}
	})
	if interactive {
		select {
		case <-authorizationReady:
		case <-done:
			t.Fatal("installer exited before the CLI authorization prompt")
		case <-ctx.Done():
			t.Fatal("CLI authorization prompt did not appear")
		}
		if _, err := terminal.Write([]byte("\r")); err != nil {
			t.Fatal(err)
		}
	}
	clientResult, isError, err := h.request("admin-test", "list_client", map[string]any{"control_token": control, "wait_ms": 20000}, 25*time.Second)
	if err != nil || isError {
		t.Fatalf("waiting for temporary client: result=%v error=%v", clientResult, err)
	}
	listed := clientResult.(map[string]any)
	clients := listed["clients"].([]any)
	if len(clients) != 1 {
		t.Fatal("temporary client did not connect")
	}
	client := clients[0].(map[string]any)
	id := client["client_id"]
	if client["client_info"].(map[string]any)["p2p"] != false {
		t.Fatal("temporary assistance must not enable the separately installed P2P component")
	}
	entries, err := os.ReadDir(temp)
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "agenterm-assistance.") {
		t.Fatalf("unexpected temporary installation: %v %v", entries, err)
	}
	workspace := filepath.Join(temp, entries[0].Name())
	if _, err := os.Stat(filepath.Join(workspace, name)); err != nil {
		t.Fatal(err)
	}
	assertEmpty(home)
	command := "printf 'preserved' > task-result.txt; printf 'session-%s' output"
	if runtime.GOOS == "windows" {
		command = "Set-Content task-result.txt preserved; Write-Output ('session-' + 'output')"
	}
	result := h.call(t, "admin-test", "execute_command", map[string]any{
		"control_token": control, "client_id": id, "command": command,
		"setup": map[string]any{"session_id": "assistance-test"},
	}, false).(map[string]any)
	if !strings.Contains(result["output"].(string), "session-output") {
		t.Fatal("temporary command output was not delivered")
	}
	if err := filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if bytes.Contains(data, []byte("session-output")) {
			return fmt.Errorf("execution output was written to %s", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.call(t, "admin-test", "close_client", map[string]any{"control_token": control}, false)
	select {
	case <-done:
		if waitErr != nil {
			t.Fatalf("temporary installer exit: %v", waitErr)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("temporary installer failed to exit after assistance ended")
	}
	if interactive {
		beforeDeny, _, found := strings.Cut(output.String(), "Deny authorization")
		if !found || !strings.HasSuffix(beforeDeny, "\r\n   ") {
			t.Fatal("the interactive authorization choices must be vertical on a wide terminal")
		}
	}
	assertEmpty(temp)
	assertEmpty(home)
	modified, err := os.ReadFile(filepath.Join(root, "task-result.txt"))
	if err != nil || strings.TrimSpace(string(modified)) != "preserved" {
		t.Fatalf("task result should remain: %q, %v", modified, err)
	}
}
