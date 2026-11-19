package service

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
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

func TestTemporaryAssistanceInvitation(t *testing.T) {
	h := newMultiHarness(t, 0)
	issued := h.call(t, "admin-test", "create_client", map[string]any{"temporary_assistance": true}, false).(map[string]any)
	deviceToken := deviceTokenFromControl(issued["control_token"].(string))
	token, err := protocol.Decode(deviceToken)
	if err != nil {
		t.Fatal(err)
	}
	code := displayShortCode(t, issued["display_text"].(string))
	get := func(path string) string {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if len(strings.TrimPrefix(path, "/c/")) == shortCodeLength {
			req.Header.Set("Accept", "text/html")
		}
		h.s.installWrapper(installerTestTag).ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s returned %d", path, w.Code)
		}
		return w.Body.String()
	}
	for _, credential := range []string{token.Secret, code} {
		page := get("/c/" + credential)
		if !strings.Contains(page, "system temporary directory") || strings.Contains(page, "agenterm-cli://") || strings.Contains(page, deviceToken) {
			t.Fatal("temporary invitation must direct the recipient to the installer")
		}
	}
	for _, suffix := range []string{".sh", ".ps1"} {
		r := httptest.NewRequest(http.MethodGet, "/c/"+code+suffix, nil)
		w := httptest.NewRecorder()
		h.s.installWrapper(installerTestTag).ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "'token' 'temporary-assistance'") {
			t.Fatalf("short-code installer %q did not preserve temporary policy: %d", suffix, w.Code)
		}
		if !strings.Contains(get("/c/"+token.Secret+suffix), "'token' 'temporary-assistance'") {
			t.Fatalf("installer %q did not receive the invitation policy", suffix)
		}
	}
	for _, credential := range []string{token.Secret, code} {
		_, err := controlCall[installerInvitation](context.Background(), h.s.testFrontend().control, "invitation", invitationRequest{
			Secret: credential, Identity: requestIdentity{RemoteAddr: "192.0.2.10:12345"},
		})
		var rejected *controlError
		if !errors.As(err, &rejected) || rejected.Status != http.StatusForbidden {
			t.Fatalf("an older frontend must be denied temporary assistance: %v", err)
		}
	}
	headers := http.Header{"Authorization": {"Bearer " + token.Secret}, version.Header: {version.Protocol}}
	conn, response, err := websocket.DefaultDialer.Dial(token.ConnectionURL(), headers)
	if conn != nil {
		conn.Close()
	}
	if response != nil {
		response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("ordinary client should be rejected, got %v", err)
	}
	invitation, err := protocol.Decode(h.s.legacyInvitationForToken(t, deviceToken))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/agenterm/c/" + code, "/agenterm/c/" + token.Secret, "/agenterm/invite/redeem"} {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.Header.Set(version.Header, version.Protocol)
		r.Header.Set("Authorization", "Bearer "+invitation.Secret)
		w := httptest.NewRecorder()
		h.s.routes(installerTestTag, nil).ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s returned %d, want forbidden", path, w.Code)
		}
	}
}

func TestTemporaryAssistanceInstallerCleanup(t *testing.T) {
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
	issued := h.call(t, "admin-test", "create_client", map[string]any{"temporary_assistance": true}, false).(map[string]any)
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
	for _, want := range []string{"Choose authorization (↑/↓/←/→, Enter):", "1. Agree to authorization    2. Deny authorization", "Enter 1 or 2:"} {
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
	if interactive {
		terminal, err = pty.Start(cmd)
		if err != nil {
			t.Fatal(err)
		}
		readDone = make(chan struct{})
		go func() {
			_, _ = io.Copy(output, terminal)
			close(readDone)
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
		if _, err := terminal.Write([]byte("\n")); err != nil {
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
	assertEmpty(temp)
	assertEmpty(home)
	modified, err := os.ReadFile(filepath.Join(root, "task-result.txt"))
	if err != nil || strings.TrimSpace(string(modified)) != "preserved" {
		t.Fatalf("task result should remain: %q, %v", modified, err)
	}
}
