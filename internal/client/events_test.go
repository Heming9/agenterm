package client

import (
	"agenterm/internal/version"
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenterm/internal/protocol"
	"github.com/gorilla/websocket"
)

// Exercise the real WebSocket, shell, and callback paths rather than deriving
// statistics from command-output strings, which can contain arbitrary text.
func TestCommandLifecycleEvents(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		name := "command"
		if interactive {
			name = "interactive"
		}
		t.Run(name, func(t *testing.T) {
			peers := make(chan *websocket.Conn, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := (&websocket.Upgrader{}).Upgrade(w, r, http.Header{version.Header: {version.Current}})
				if err == nil {
					peers <- ws
				}
			}))
			defer server.Close()
			token, err := protocol.Encode(protocol.Token{
				URL:    "ws" + strings.TrimPrefix(server.URL, "http") + "/agenterm/client",
				Secret: base64.RawURLEncoding.EncodeToString(make([]byte, protocol.TokenSecretSize)),
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events := make(chan CommandEvent, 16)
			result := make(chan error, 1)
			go func() {
				result <- RunWithCallbacks(ctx, token, io.Discard, Callbacks{
					Command: func(event CommandEvent) { events <- event },
				})
			}()
			var peer *websocket.Conn
			select {
			case peer = <-peers:
			case <-time.After(5 * time.Second):
				t.Fatal("client did not connect")
			}
			defer peer.Close()
			peer.SetReadDeadline(time.Now().Add(5 * time.Second))
			// Run both commands concurrently. A failing command also counts as
			// completed, and its duration must stay associated with its own ID.
			for _, m := range []protocol.Message{
				{Type: "exec", ID: "success", Command: "echo hello", Interactive: interactive},
				{Type: "exec", ID: "failure", Command: "exit 7", Interactive: interactive},
			} {
				if err := peer.WriteJSON(m); err != nil {
					t.Fatal(err)
				}
			}
			done := map[string]int{}
			for len(done) < 2 {
				var m protocol.Message
				if err := peer.ReadJSON(&m); err != nil {
					t.Fatal(err)
				}
				if m.Type == "done" {
					done[m.ID] = m.ExitCode
				}
			}
			if done["success"] != 0 || done["failure"] != 7 {
				t.Fatalf("unexpected exit codes: %v", done)
			}
			started := map[string]CommandEvent{}
			finished := map[string]CommandEvent{}
			for i := 0; i < 4; i++ {
				select {
				case event := <-events:
					if event.Done {
						finished[event.ID] = event
					} else {
						started[event.ID] = event
					}
				case <-time.After(5 * time.Second):
					t.Fatal("missing lifecycle event")
				}
			}
			for id, code := range done {
				start, end := started[id], finished[id]
				if start.ID != id || end.ID != id || end.Duration <= 0 || end.ExitCode != code ||
					start.StartedAt.IsZero() || start.StartedAt != end.StartedAt || end.Command != start.Command || end.Interactive != interactive {
					t.Fatalf("invalid lifecycle for %s: start=%+v end=%+v", id, start, end)
				}
			}
			cancel()
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("client did not disconnect")
			}
		})
	}
}

func TestCommandMarkersRemainWithoutCallbacks(t *testing.T) {
	var log bytes.Buffer
	c := &connection{log: &log}
	start := c.commandStarted(protocol.Message{ID: "test", Command: "echo hello"})
	c.commandFinished(start, 0, "")
	if !strings.Contains(log.String(), "[Agenterm] Running: echo hello") ||
		!strings.Contains(log.String(), "[Agenterm] Finished (exit code: 0)") {
		t.Fatalf("CLI command markers changed: %q", log.String())
	}
}
