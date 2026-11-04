package client

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func terminalAPIPeer(t *testing.T, callbacks Callbacks) *websocket.Conn {
	t.Helper()
	peers := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, http.Header{protocol.ClientAPIHeader: {"1"}, version.Header: {version.Current}})
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
	go func() { ended <- RunWithCallbacks(ctx, token, io.Discard, callbacks) }()
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

func callTerminalAPI(t *testing.T, peer *websocket.Conn, method string, args any, out any) string {
	t.Helper()
	params, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(APIRequest{Method: method, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.WriteJSON(protocol.Message{Type: "api_call", ID: "request", Request: request}); err != nil {
		t.Fatal(err)
	}
	reply := readTerminalLimitMessage(t, peer, "request", "api_result")
	if reply.Error == "" && out != nil {
		if err := json.Unmarshal(reply.Data, out); err != nil {
			t.Fatal(err)
		}
	}
	return reply.Error
}
func TestTerminalAPIOverWebSocket(t *testing.T) {
	peer := terminalAPIPeer(t, Callbacks{})
	var out Output
	if err := callTerminalAPI(t, peer, "execute", ExecuteRequest{Command: "echo transported"}, &out); err != "" {
		t.Fatal(err)
	}
	if out.SessionID != "" || out.Status != "closed" || !strings.Contains(out.Output, "transported") {
		t.Fatal(out)
	}
	var list ListResult
	if err := callTerminalAPI(t, peer, "list", struct{}{}, &list); err != "" || len(list.Sessions) != 1 {
		t.Fatalf("%+v %s", list, err)
	}
	var read ReadOutput
	if err := callTerminalAPI(t, peer, "read", ReadRequest{SessionID: list.Sessions[0].SessionID}, &read); err != "" || read.Output != out.Output {
		t.Fatalf("%+v %s", read, err)
	}
	if err := callTerminalAPI(t, peer, "execute", map[string]any{"command": "echo forbidden", "legacy": true}, nil); err == "" {
		t.Fatal("accepted unknown API field")
	}
}

func TestBootstrapKeepsOutputAndExitCodeLocal(t *testing.T) {
	output := make(chan string, 8)
	events := make(chan CommandEvent, 2)
	peer := terminalAPIPeer(t, Callbacks{Output: func(_ string, data []byte) { output <- string(data) }, Command: func(e CommandEvent) { events <- e }})
	script := "printf bootstrap-output; exit 7"
	if runtime.GOOS == "windows" {
		script = "Write-Output bootstrap-output; exit 7"
	}
	if err := peer.WriteJSON(protocol.Message{Type: "p2p_start", ID: "bootstrap", P2P: &protocol.P2PLaunch{Script: script}}); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(10 * time.Second))
	var message map[string]any
	if err := peer.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	if len(message) != 2 || message["type"] != "p2p_done" || message["id"] != "bootstrap" {
		t.Fatalf("bootstrap sent unexpected data: %v", message)
	}
	select {
	case text := <-output:
		if !strings.Contains(text, "bootstrap-output") {
			t.Fatal(text)
		}
	case <-time.After(time.Second):
		t.Fatal("missing local output")
	}
	for range 2 {
		select {
		case event := <-events:
			if event.Done && event.ExitCode != 7 {
				t.Fatal(event)
			}
		case <-time.After(time.Second):
			t.Fatal("missing local command event")
		}
	}
}
