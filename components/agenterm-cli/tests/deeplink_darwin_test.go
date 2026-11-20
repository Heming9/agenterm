//go:build darwin

package interop

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Opt-in only: registers the real per-user handler and opens a Terminal window.
// Ordinary test runs use private homes and never alter OS protocol registration.
func TestRustNativeMacProtocolLaunch(t *testing.T) {
	if os.Getenv("AGENTERM_TEST_NATIVE_LINK") != "1" {
		t.Skip("set AGENTERM_TEST_NATIVE_LINK=1 to exercise macOS Launch Services and Terminal")
	}
	binary := os.Getenv("AGENTERM_RUST_CLI")
	if binary == "" {
		t.Fatal("AGENTERM_RUST_CLI is required")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	registration := exec.Command(binary, "-version")
	registration.Env = append(os.Environ(), "AGENTERM_SKIP_LINK_REGISTRATION=0")
	out, err := registration.CombinedOutput()
	if err != nil || strings.Contains(string(out), "link registration:") {
		t.Fatalf("register handler: %v %s", err, out)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(config, "Agenterm", "cli", "agenterm-cli-"+hex.EncodeToString(sum[:]))
	before := nativeLinkProcesses(t, stable)
	t.Cleanup(func() {
		for pid := range nativeLinkProcesses(t, stable) {
			if !before[pid] {
				if process, e := os.FindProcess(pid); e == nil {
					process.Signal(os.Interrupt)
				}
			}
		}
	})
	peers := make(chan *websocket.Conn, 1)
	var token string
	tokenReady := make(chan struct{})
	secret := base64.RawURLEncoding.EncodeToString([]byte("native-key"))
	mux := http.NewServeMux()
	mux.HandleFunc("/agenterm/c/ABC123", func(w http.ResponseWriter, r *http.Request) {
		<-tokenReady
		if r.Method != "POST" {
			http.Error(w, "method", 405)
			return
		}
		w.Header().Set(version.Header, version.Protocol)
		json.NewEncoder(w).Encode(map[string]string{"token": token})
	})
	mux.HandleFunc("/agenterm/client/abc123", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "unauthorized", 401)
			return
		}
		headers := http.Header{version.Header: {version.Protocol}, protocol.ClientAPIHeader: {"1"}}
		peer, e := (&websocket.Upgrader{}).Upgrade(w, r, headers)
		if e == nil {
			peers <- peer
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	token, err = protocol.Encode(protocol.Token{URL: "ws" + strings.TrimPrefix(server.URL, "http") + "/agenterm/client", Secret: secret, Identifier: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	close(tokenReady)
	raw := "agenterm-cli://authorize?" + url.Values{"url": {server.URL + "/agenterm/c/ABC123"}}.Encode()
	t.Log("In the Terminal window that opens, approve the loopback connection in the Agenterm CLI.")
	if out, err := exec.Command("/usr/bin/open", raw).CombinedOutput(); err != nil {
		t.Fatalf("open protocol URL: %v %s", err, out)
	}
	var peer *websocket.Conn
	select {
	case peer = <-peers:
	case <-time.After(30 * time.Second):
		t.Fatal("macOS did not connect the Rust client after local authorization")
	}
	defer peer.Close()
	running := nativeLinkProcesses(t, stable)
	found := false
	for pid := range running {
		if !before[pid] {
			found = true
		}
	}
	if !found {
		t.Fatal("connection did not come from the registered stable Rust executable")
	}
	request, _ := json.Marshal(map[string]any{"method": "execute", "params": map[string]any{"command": "printf 'Rust protocol launch verified\\n'", "wait_ms": 5000}})
	if err := peer.WriteJSON(protocol.Message{Type: "api_call", ID: "native-link", Request: request}); err != nil {
		t.Fatal(err)
	}
	peer.SetReadDeadline(time.Now().Add(8 * time.Second))
	var reply protocol.Message
	if err := peer.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != "" || !strings.Contains(string(reply.Data), "Rust protocol launch verified") {
		t.Fatalf("linked client execution failed: %+v", reply)
	}
	peer.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
}

func nativeLinkProcesses(t *testing.T, stable string) map[int]bool {
	t.Helper()
	out, err := exec.Command("/bin/ps", "-axww", "-o", "pid=", "-o", "command=").Output()
	if err != nil {
		t.Fatal(err)
	}
	result := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		at := strings.IndexByte(line, ' ')
		if at < 0 {
			continue
		}
		pid, e := strconv.Atoi(line[:at])
		command := strings.TrimSpace(line[at:])
		if e == nil && strings.HasPrefix(command, fmt.Sprintf("%s -link-file ", stable)) {
			result[pid] = true
		}
	}
	return result
}
