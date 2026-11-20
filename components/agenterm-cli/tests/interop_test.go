package interop

// Exercise the Rust executable with Go's real token encoder and WebSocket
// implementation. No production service, credentials, or trust-store writes.
import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fixture struct {
	t              *testing.T
	peers          chan *websocket.Conn
	peer           *websocket.Conn
	sequence       int
	stdout, stderr bytes.Buffer
	command        *exec.Cmd
	done           chan error
	resumes        atomic.Int32
	redemptions    atomic.Int32
}

func newFixture(t *testing.T, tls bool, mode string) *fixture {
	t.Helper()
	binary := os.Getenv("AGENTERM_RUST_CLI")
	if binary == "" {
		t.Skip("set AGENTERM_RUST_CLI to the Rust release executable")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	persistent := mode == "persistent"
	f := &fixture{t: t, peers: make(chan *websocket.Conn, 4), done: make(chan error, 1)}
	secret := base64.RawURLEncoding.EncodeToString([]byte("device-key"))
	var token string
	tokenReady := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/prototype/c/ABC123", func(w http.ResponseWriter, r *http.Request) {
		<-tokenReady
		if r.Method == http.MethodHead {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method", 405)
			return
		}
		f.redemptions.Add(1)
		w.Header().Set(version.Header, version.Protocol)
		json.NewEncoder(w).Encode(map[string]string{"token": token})
	})
	mux.HandleFunc("/prototype/client/abc123", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get(version.Header) != version.Protocol || r.Header.Get(protocol.ClientAPIHeader) != "1" {
			http.Error(w, "unauthorized", 401)
			return
		}
		if advertised := r.Header.Get(protocol.P2PCapabilityHeader) == "1"; advertised != persistent {
			t.Errorf("P2P advertised = %v, persistent connection = %v", advertised, persistent)
		}
		if r.Header.Get(protocol.ResumeCredentialHeader) == "resume-test" {
			f.resumes.Add(1)
		}
		headers := http.Header{version.Header: {version.Protocol}, protocol.ClientAPIHeader: {"1"}, protocol.HeartbeatHeader: {"1"}, protocol.ResumeCredentialHeader: {"resume-test"}}
		peer, err := (&websocket.Upgrader{}).Upgrade(w, r, headers)
		if err != nil {
			return
		}
		f.peers <- peer
	})
	server := httptest.NewUnstartedServer(mux)
	if tls {
		server.StartTLS()
	} else {
		server.Start()
	}
	t.Cleanup(server.Close)
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/prototype/client"
	token, err = protocol.Encode(protocol.Token{URL: url, Secret: secret, Identifier: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	close(tokenReady)
	address := server.URL + "/prototype/c/ABC123"
	args := []string{"-token", address}
	if persistent {
		args = append(args, "-connection-mode", "persistent")
	}
	if strings.HasPrefix(mode, "link-") {
		query := neturl.Values{"url": {address}}
		if mode != "link-default" {
			query.Set("mode", strings.TrimPrefix(mode, "link-"))
		}
		args = []string{"agenterm-cli://connect?" + query.Encode()}
	}
	f.command = exec.Command(binary, args...)
	home := t.TempDir()
	f.command.Env = append(os.Environ(), "AGENTERM_SKIP_LINK_REGISTRATION=1", "HOME="+home, "USERPROFILE="+home, "AGENTERM_LANG=zh-CN", "NO_COLOR=1", "SHELL=/bin/sh")
	if tls {
		path := filepath.Join(home, "ca.pem")
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
			t.Fatal(err)
		}
		f.command.Env = append(f.command.Env, "AGENTERM_CA_FILE="+path)
	}
	input := "1\n"
	if mode == "deny" {
		input = "3\n"
	}
	f.command.Stdin = strings.NewReader(input)
	f.command.Stdout = &f.stdout
	f.command.Stderr = &f.stderr
	if err := f.command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { f.done <- f.command.Wait() }()
	t.Cleanup(func() {
		if f.peer != nil {
			_ = f.peer.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			_ = f.peer.Close()
		}
		select {
		case err := <-f.done:
			if err != nil {
				t.Errorf("Rust CLI failed: %v\n%s\n%s", err, f.stdout.String(), f.stderr.String())
			}
		case <-time.After(5 * time.Second):
			_ = f.command.Process.Kill()
			<-f.done
			t.Errorf("Rust CLI did not shut down\n%s\n%s", f.stdout.String(), f.stderr.String())
		}
	})
	if mode != "deny" {
		f.nextPeer()
	}
	return f
}
func (f *fixture) nextPeer() {
	f.t.Helper()
	select {
	case f.peer = <-f.peers:
	case <-time.After(12 * time.Second):
		f.t.Fatal("Rust CLI did not connect")
	}
	_ = f.peer.SetReadDeadline(time.Now().Add(5 * time.Second))
}
func (f *fixture) call(method string, params any) map[string]any {
	f.t.Helper()
	f.sequence++
	id := fmt.Sprint(f.sequence)
	raw, _ := json.Marshal(map[string]any{"method": method, "params": params})
	if err := f.peer.WriteJSON(protocol.Message{Type: "api_call", ID: id, Request: raw}); err != nil {
		f.t.Fatal(err)
	}
	_ = f.peer.SetReadDeadline(time.Now().Add(10 * time.Second))
	var reply protocol.Message
	if err := f.peer.ReadJSON(&reply); err != nil {
		f.t.Fatal(err)
	}
	if reply.ID != id || reply.Type != "api_result" || reply.Error != "" {
		f.t.Fatalf("bad response: %+v", reply)
	}
	var data map[string]any
	if err := json.Unmarshal(reply.Data, &data); err != nil {
		f.t.Fatal(err)
	}
	return data
}

func TestRustCLIProtocol(t *testing.T) {
	shell := func(unix, windows string) string {
		if runtime.GOOS == "windows" {
			return windows
		}
		return unix
	}
	for _, tls := range []bool{false, true} {
		t.Run(fmt.Sprint("TLS=", tls), func(t *testing.T) {
			f := newFixture(t, tls, "short-code")
			// Ping/pong must remain responsive even with a waiting terminal call.
			pong := false
			f.peer.SetPongHandler(func(value string) error { pong = value == "probe"; return nil })
			if err := f.peer.WriteControl(websocket.PingMessage, []byte("probe"), time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			first := f.call("execute", map[string]any{"command": shell("export PROTOTYPE=kept; printf '你好\\n'", "$env:PROTOTYPE='kept'; Write-Output '你好'"), "setup": map[string]any{"session_id": "persistent"}})
			if !pong || first["status"] != "idle" || !strings.Contains(first["output"].(string), "你好") {
				t.Fatalf("first=%v pong=%v", first, pong)
			}
			second := f.call("execute", map[string]any{"command": shell("printf '%s' \"$PROTOTYPE\"; false", "Write-Output $env:PROTOTYPE; cmd /c exit 1"), "session_id": "persistent"})
			if second["exit_code"] != float64(1) || !strings.Contains(second["output"].(string), "kept") {
				t.Fatal(second)
			}
			interactive := f.call("execute", map[string]any{"command": shell("read answer; printf 'answer:%s' \"$answer\"", "$answer=Read-Host; Write-Output ('answer:'+$answer)"), "wait_ms": 0})
			id := interactive["session_id"].(string)
			f.call("control", map[string]any{"session_id": id, "action": "input", "input": "yes\n"})
			var output string
			cursor := float64(0)
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				r := f.call("read", map[string]any{"session_id": id, "cursor": cursor, "wait_ms": 1000})
				output += r["output"].(string)
				cursor = r["next_cursor"].(float64)
				if r["status"] == "idle" {
					break
				}
			}
			if !strings.Contains(output, "answer:yes") {
				t.Fatalf("interactive output=%q", output)
			}
			if f.call("control", map[string]any{"session_id": id, "action": "stop"})["status"] != "closed" {
				t.Fatal("session did not stop")
			}
			// A broken transport must preserve a named shell and resume credential.
			_ = f.peer.Close()
			f.nextPeer()
			if f.resumes.Load() != 1 {
				t.Fatal("missing resume credential")
			}
			third := f.call("execute", map[string]any{"command": shell("printf '%s' \"$PROTOTYPE\"", "Write-Output $env:PROTOTYPE"), "session_id": "persistent"})
			if !strings.Contains(third["output"].(string), "kept") {
				t.Fatal(third)
			}
			quick := f.call("execute", map[string]any{"command": shell("printf quick", "Write-Output quick")})
			if quick["status"] != "closed" || quick["session_id"] != nil || quick["exit_code"] != float64(0) {
				t.Fatal(quick)
			}
			listed := f.call("list", map[string]any{})
			if len(listed["sessions"].([]any)) < 3 {
				t.Fatal(listed)
			}
		})
	}
}

func TestRustCLIDeniesShortCodeURL(t *testing.T) {
	f := newFixture(t, false, "deny")
	select {
	case p := <-f.peers:
		_ = p.Close()
		t.Fatal("denied invitation connected")
	case <-time.After(200 * time.Millisecond):
	}
	if f.redemptions.Load() != 0 {
		t.Fatal("denying authorization redeemed the short code")
	}
	// Cleanup waits for the process and checks its exit status.
}

func TestRustCLIProtocolLinks(t *testing.T) {
	for _, mode := range []string{"link-default", "link-temporary", "link-persistent"} {
		for _, tls := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/TLS=%v", mode, tls), func(t *testing.T) {
				f := newFixture(t, tls, mode)
				command := "printf 'protocol-link-ok'"
				if runtime.GOOS == "windows" {
					command = "Write-Output 'protocol-link-ok'"
				}
				result := f.call("execute", map[string]any{"command": command, "wait_ms": 5000})
				if !strings.Contains(result["output"].(string), "protocol-link-ok") {
					t.Fatal(result)
				}
			})
		}
	}
}
