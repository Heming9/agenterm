package client

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func terminalLimitPeer(t *testing.T) *websocket.Conn {
	t.Helper()
	peers := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, http.Header{version.Header: {version.Current}})
		if err == nil {
			peers <- ws
		}
	}))
	t.Cleanup(server.Close)
	token, err := protocol.Encode(protocol.Token{URL: "ws" + strings.TrimPrefix(server.URL, "http") + "/agenterm/client", Secret: base64.RawURLEncoding.EncodeToString(make([]byte, protocol.TokenSecretSize))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() { ended <- RunWithCallbacks(ctx, token, io.Discard, Callbacks{}) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-ended:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("client did not release terminal processes")
		}
	})
	select {
	case peer := <-peers:
		t.Cleanup(func() { _ = peer.Close() })
		return peer
	case <-time.After(5 * time.Second):
		t.Fatal("client did not connect")
		return nil
	}
}

func readTerminalLimitMessage(t *testing.T, peer *websocket.Conn, id, kind string) protocol.Message {
	t.Helper()
	_ = peer.SetReadDeadline(time.Now().Add(20 * time.Second))
	for {
		var message protocol.Message
		if err := peer.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
		if message.ID == id && message.Type == kind {
			return message
		}
	}
}

// Use raw WebSocket commands to exercise the quota authority directly. Two
// connections in the same process must share the final client-side budget.
func TestTerminalLimitEnforcedByClientProcess(t *testing.T) {
	first, second := terminalLimitPeer(t), terminalLimitPeer(t)
	send := func(peer *websocket.Conn, m protocol.Message) {
		t.Helper()
		if err := peer.WriteJSON(m); err != nil {
			t.Fatal(err)
		}
	}
	send(first, protocol.Message{Type: "session_exec", ID: "missing", RequestID: "missing", SessionID: "unknown", Command: "echo must-not-run"})
	if r := readTerminalLimitMessage(t, first, "missing", "command_rejected"); r.Error == "" {
		t.Fatal(r)
	}
	var terminals []string
	for i := 0; i < 10; i++ {
		request := fmt.Sprintf("request-%d", i)
		send(first, protocol.Message{Type: "session_exec", ID: request, RequestID: request, Setup: &protocol.SessionSetup{SessionID: request}, Command: "echo slot-ready"})
		ack := readTerminalLimitMessage(t, first, request, "command_accepted")
		if ack.SessionID == "" || !ack.SessionCreate {
			t.Fatal(ack)
		}
		terminals = append(terminals, ack.SessionID)
		if r := readTerminalLimitMessage(t, first, ack.SessionID, "command_done"); r.ExitCode != 0 || r.Error != "" {
			t.Fatal(r)
		}
	}
	send(second, protocol.Message{Type: "session_exec", ID: "over-limit", RequestID: "over-limit", Setup: &protocol.SessionSetup{SessionID: "extra"}, Command: "echo must-not-run"})
	rejected := readTerminalLimitMessage(t, second, "over-limit", "command_rejected")
	if !strings.Contains(rejected.Error, "10") {
		t.Fatal(rejected)
	}
	send(first, protocol.Message{Type: "session_exec", ID: "implicit", RequestID: "implicit", Command: "exit 7"})
	if r := readTerminalLimitMessage(t, first, "implicit", "command_rejected"); !strings.Contains(r.Error, "10") {
		t.Fatal(r)
	}
	send(first, protocol.Message{Type: "session_exec", ID: "duplicate", RequestID: "duplicate", Setup: &protocol.SessionSetup{SessionID: terminals[0]}, Command: "echo must-not-run"})
	if r := readTerminalLimitMessage(t, first, "duplicate", "command_rejected"); r.Error == "" {
		t.Fatal(r)
	}
	send(first, protocol.Message{Type: "session_exec", ID: "conflict", RequestID: "conflict", SessionID: terminals[0], Setup: &protocol.SessionSetup{}, Command: "echo must-not-run"})
	if r := readTerminalLimitMessage(t, first, "conflict", "command_rejected"); r.Error == "" {
		t.Fatal(r)
	}
	send(first, protocol.Message{Type: "session_exec", ID: "explicit", RequestID: "explicit", SessionID: terminals[0], Command: "echo reused"})
	ack := readTerminalLimitMessage(t, first, "explicit", "command_accepted")
	if ack.SessionID != terminals[0] || ack.SessionCreate {
		t.Fatal(ack)
	}
	if r := readTerminalLimitMessage(t, first, terminals[0], "command_done"); r.Error != "" || r.ExitCode != 0 {
		t.Fatal(r)
	}
	send(first, protocol.Message{Type: "session_exec", ID: "named-reuse", RequestID: "named-reuse", SessionID: "request-0", Command: "echo named"})
	named := readTerminalLimitMessage(t, first, "named-reuse", "command_accepted")
	if named.SessionID != terminals[0] || named.SessionCreate {
		t.Fatal(named)
	}
	if r := readTerminalLimitMessage(t, first, terminals[0], "command_done"); r.Error != "" || r.ExitCode != 0 {
		t.Fatal(r)
	}
	send(first, protocol.Message{Type: "cancel", ID: terminals[0]})
	if r := readTerminalLimitMessage(t, first, terminals[0], "done"); !r.Canceled {
		t.Fatal(r)
	}
	send(second, protocol.Message{Type: "session_exec", ID: "failed-start", RequestID: "failed-start", Setup: &protocol.SessionSetup{SessionID: "extra", ExecOptions: protocol.ExecOptions{Cwd: filepath.Join(t.TempDir(), "missing")}}, Command: "echo must-not-run"})
	failed := readTerminalLimitMessage(t, second, "failed-start", "command_accepted")
	if r := readTerminalLimitMessage(t, second, failed.SessionID, "done"); r.Error == "" {
		t.Fatal(r)
	}
	send(second, protocol.Message{Type: "session_exec", ID: "replacement", RequestID: "replacement", Setup: &protocol.SessionSetup{SessionID: "extra"}, Command: "echo replacement"})
	replacement := readTerminalLimitMessage(t, second, "replacement", "command_accepted")
	if r := readTerminalLimitMessage(t, second, replacement.SessionID, "command_done"); r.Error != "" || r.ExitCode != 0 {
		t.Fatal(r)
	}
}
