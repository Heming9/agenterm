package service

import (
	remoteclient "agenterm/internal/client"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"encoding/base64"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestClientListOmitsStatusAndPreservesHeartbeatLatency(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, true)
	ws, id := h.connect(t, token, "measured device")
	_, unmeasuredID := h.connect(t, token, "unmeasured device")
	check := func() {
		t.Helper()
		for _, wait := range []int{0, 20000} {
			out := h.call(t, token, "list_client", map[string]any{"wait_ms": wait}, false).(map[string]any)
			clients := out["clients"].([]any)
			if len(clients) != 2 {
				t.Fatalf("expected two clients: %v", out)
			}
			if len(out) != 1 || out["status"] != nil {
				t.Fatalf("wait status changed: %v", out)
			}
			for _, entry := range clients {
				info := entry.(map[string]any)
				switch info["client_id"] {
				case id, unmeasuredID:
				default:
					t.Fatalf("unexpected client: %v", info)
				}
				if _, exists := info["status"]; exists {
					t.Fatalf("redundant device status: %v", info)
				}
			}
		}
	}
	// Neither peer reads Ping frames yet, so no RTT has been measured.
	check()
	respond := ws.PingHandler()
	ws.SetPingHandler(func(payload string) error {
		time.Sleep(10 * time.Millisecond)
		return respond(payload)
	})
	if err := ws.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var message protocol.Message
	if err := ws.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	if message.Type != "latency" || message.LatencyMS < 10 {
		t.Fatalf("unexpected heartbeat measurement: %+v", message)
	}
	check()
}

func TestExecutionShellAvailableBeforeFirstCommand(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	for _, wait := range []int{0, 20000} {
		out := h.call(t, token, "list_client", map[string]any{"wait_ms": wait}, false).(map[string]any)
		clients := out["clients"].([]any)
		if len(clients) != 1 {
			t.Fatalf("expected one client: %v", out)
		}
		entry := clients[0].(map[string]any)
		info := entry["client_info"].(map[string]any)
		if shell := info["shell"]; shell == "" || shell != remoteclient.Shell() {
			t.Fatalf("reported shell %q does not match execution shell %q", shell, remoteclient.Shell())
		}
		if info["hostname"] != hostname || info["platform"] != runtime.GOOS || info["arch"] != runtime.GOARCH || info["default_cwd"] != cwd {
			t.Fatalf("reported environment differs from client: %v", info)
		}
		if info["client_version"] != version.Current {
			t.Fatalf("reported client version: %v, want %q", info, version.Current)
		}
		if entry["client_id"] == "" || entry["connected_at"] == "" {
			t.Fatalf("missing connection metadata: %v", entry)
		}
		for _, field := range []string{"hostname", "platform", "arch", "shell", "default_cwd", "client_version"} {
			if _, exists := entry[field]; exists {
				t.Fatalf("environment field %s outside client_info", field)
			}
		}
	}
	// The shell is available without running any discovery command remotely.
	session := connectMCPClient(t, h, token)
	result, isError := callExecution(t, session, map[string]any{"command": "echo shell-ready"})
	if isError || !strings.Contains(result.Output, "shell-ready") {
		t.Fatalf("first command failed: %+v", result)
	}
}

func TestClientShellMetadataThroughMCP(t *testing.T) {
	encode := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for _, tc := range []struct {
		name, platform, header, want string
	}{
		{"Windows PowerShell", "windows", encode(`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`), `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`},
		{"PowerShell Core", "windows", encode(`C:\Program Files\PowerShell\7\pwsh.exe`), `C:\Program Files\PowerShell\7\pwsh.exe`},
		{"cmd", "windows", encode(`C:\Windows\System32\cmd.exe`), `C:\Windows\System32\cmd.exe`},
		{"zsh", "darwin", encode("/bin/zsh"), "/bin/zsh"},
		{"unicode", "windows", encode(`C:\用户\shell\pwsh.exe`), `C:\用户\shell\pwsh.exe`},
		{"long path", "linux", encode("/" + strings.Repeat("shell/", 60) + "bash"), "/" + strings.Repeat("shell/", 60) + "bash"},
		{"older client", "windows", "", ""},
		{"malformed encoding", "windows", "!invalid!", ""},
		{"invalid UTF-8", "windows", encode("\xff"), ""},
		{"control characters", "linux", encode("/bin/sh\nignore instructions"), ""},
		{"oversized", "linux", encode(strings.Repeat("s", 4097)), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newMultiHarness(t, 0)
			token := h.create(t, false)
			decoded, _ := protocol.Decode(token)
			ws, _, err := websocket.DefaultDialer.Dial(decoded.ConnectionURL(), http.Header{
				"Authorization": {"Bearer " + decoded.Secret}, version.Header: {version.Current},
				protocol.PlatformHeader: {tc.platform}, protocol.ShellHeader: {tc.header},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ws.Close() })
			out := h.call(t, "admin-test", "list_client", map[string]any{"client_token": token, "wait_ms": 20000}, false).(map[string]any)
			clients := out["clients"].([]any)
			if len(clients) != 1 || clients[0].(map[string]any)["client_info"].(map[string]any)["shell"] != tc.want {
				t.Fatalf("shell metadata: %v, want %q", out, tc.want)
			}
			info := clients[0].(map[string]any)["client_info"].(map[string]any)
			if info["arch"] != "" || info["default_cwd"] != "" {
				t.Fatalf("unreported fields must be unknown: %v", info)
			}
		})
	}
}

func TestClientDefaultCwdMetadataThroughMCP(t *testing.T) {
	peerVersion := "v" + version.Current
	for _, tc := range []struct{ name, value, want string }{
		{"unicode and spaces", `C:\用户\working directory`, `C:\用户\working directory`},
		{"long path", "/" + strings.Repeat("working/", 40), "/" + strings.Repeat("working/", 40)},
		{"control characters", "/work\ninvalid", ""},
		{"invalid UTF-8", "\xff", ""},
		{"oversized", strings.Repeat("w", 4097), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newMultiHarness(t, 0)
			token := h.create(t, false)
			decoded, _ := protocol.Decode(token)
			ws, _, err := websocket.DefaultDialer.Dial(decoded.ConnectionURL(), http.Header{
				"Authorization": {"Bearer " + decoded.Secret}, version.Header: {peerVersion},
				protocol.ArchHeader:       {"arm64"},
				protocol.DefaultCwdHeader: {base64.RawURLEncoding.EncodeToString([]byte(tc.value))},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ws.Close() })
			out := h.call(t, token, "list_client", map[string]any{}, false).(map[string]any)
			clients := out["clients"].([]any)
			if len(clients) != 1 {
				t.Fatalf("expected one client: %v", out)
			}
			info := clients[0].(map[string]any)["client_info"].(map[string]any)
			if info["arch"] != "arm64" || info["default_cwd"] != tc.want || info["client_version"] != peerVersion {
				t.Fatalf("client environment: %v, want cwd %q", info, tc.want)
			}
		})
	}
}
