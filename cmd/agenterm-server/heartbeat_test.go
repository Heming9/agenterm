package main

import (
	remoteclient "agenterm/internal/client"
	"agenterm/internal/protocol"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func heartbeatPeer(t *testing.T, interval, timeout time.Duration) (*websocket.Conn, <-chan error) {
	t.Helper()
	ended := make(chan error, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			ended <- err
			return
		}
		c := &client{conn: ws}
		stop := c.startHeartbeat(interval, timeout)
		defer func() { ws.Close(); stop() }()
		for {
			var m protocol.Message
			if err := ws.ReadJSON(&m); err != nil {
				ended <- err
				return
			}
		}
	}))
	t.Cleanup(s.Close)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws, ended
}

func TestHeartbeatAndReconnectWithRealClient(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished, latency := make(chan error, 1), make(chan time.Duration, 4)
	go func() {
		finished <- remoteclient.RunWithCallbacks(ctx, token, io.Discard, remoteclient.Callbacks{Latency: func(d time.Duration) { latency <- d }})
	}()
	waitClient := func(previous string) clientInfo {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		decoded, _ := protocol.Decode(token)
		for time.Now().Before(deadline) {
			clients := h.s.listClients(decoded.Secret)
			if len(clients) == 1 && clients[0].ID != previous {
				return clients[0]
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("client did not reconnect")
		return clientInfo{}
	}
	first := waitClient("")
	select {
	case d := <-latency:
		if d <= 0 {
			t.Fatal("missing RTT")
		}
	case <-time.After(time.Second):
		t.Fatal("first RTT not measured immediately")
	}
	h.s.mu.Lock()
	old := h.s.clients[first.ID]
	h.s.mu.Unlock()
	old.conn.Close() // Simulate transport loss without restarting or losing tokens.
	second := waitClient(first.ID)
	decoded, _ := protocol.Decode(token)
	out, err := h.s.execute(ctx, decoded.Secret, second.ID, "echo recovered", protocol.ExecOptions{})
	if err != nil || !strings.Contains(out.Stdout, "recovered") {
		t.Fatalf("reconnected command: %+v %v", out, err)
	}
	select {
	case <-latency:
	case <-time.After(time.Second):
		t.Fatal("RTT not refreshed after reconnect")
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not stop client")
	}
}

func TestHeartbeatKeepsIdleConnectionAliveAndReportsRTT(t *testing.T) {
	ws, ended := heartbeatPeer(t, 20*time.Millisecond, 200*time.Millisecond)
	// Delay Pong so the measured value demonstrably includes the round trip.
	respond := ws.PingHandler()
	ws.SetPingHandler(func(payload string) error { time.Sleep(8 * time.Millisecond); return respond(payload) })
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	started, samples := time.Now(), 0
	for time.Since(started) < 450*time.Millisecond {
		var m protocol.Message
		if err := ws.ReadJSON(&m); err != nil {
			t.Fatal(err)
		}
		if m.Type != "latency" || m.LatencyMS < 8 {
			t.Fatalf("bad RTT: %+v", m)
		}
		samples++
	}
	if samples < 3 {
		t.Fatalf("only %d samples", samples)
	}
	select {
	case err := <-ended:
		t.Fatalf("healthy peer disconnected: %v", err)
	default:
	}
	ws.Close()
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not stop")
	}
}

func TestHeartbeatTimesOutMissingOrUnmatchedPong(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "unmatched"}[mismatch], func(t *testing.T) {
			ws, ended := heartbeatPeer(t, 20*time.Millisecond, 120*time.Millisecond)
			ws.SetPingHandler(func(string) error {
				if mismatch {
					return ws.WriteControl(websocket.PongMessage, []byte("wrong"), time.Now().Add(time.Second))
				}
				return nil
			})
			ws.SetReadDeadline(time.Now().Add(time.Second))
			var m protocol.Message
			if err := ws.ReadJSON(&m); err == nil {
				t.Fatal("silent peer remained connected")
			}
			select {
			case err := <-ended:
				if e, ok := err.(net.Error); !ok || !e.Timeout() {
					t.Fatalf("want timeout, got %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("server did not expire peer")
			}
		})
	}
}
