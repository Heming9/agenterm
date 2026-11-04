package service

import (
	terminal "agenterm/internal/client"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClientAPICapability(t *testing.T) {
	for _, v := range []string{"", "unsupported", terminal.APIVersion} {
		t.Run(v, func(t *testing.T) {
			h := newMultiHarness(t, 0)
			token := h.create(t, false)
			decoded, _ := protocol.Decode(token)
			ws, _, err := websocket.DefaultDialer.Dial(decoded.ConnectionURL(), http.Header{"Authorization": {"Bearer " + decoded.Secret}, version.Header: {version.Current}, protocol.ClientAPIHeader: {v}})
			if err != nil {
				t.Fatal(err)
			}
			defer ws.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := h.s.waitForClients(ctx, decoded.Secret, time.Second); err != nil {
				t.Fatal(err)
			}
			if v == terminal.APIVersion {
				go serveTestDevice(wrapTestDevice(ws), "supported", nil, nil)
			}
			h.call(t, token, "execute_command", map[string]any{"command": "probe"}, v != terminal.APIVersion)
		})
	}
}
func TestSessionReadLimitThroughMCP(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, _ := h.connect(t, token, "paged")
	api := newFixtureAPI("paged")
	api.sessions["paged"] = terminal.ReadOutput{SessionID: "paged", CommandID: "command", Status: "closed", Output: strings.Repeat("x", 2*terminal.MaxReadBytes), ExitCode: new(7)}
	go serveAPI(ws, api)
	out := h.call(t, token, "session_read", map[string]any{"session_id": "paged"}, false).(map[string]any)
	if len(out["output"].(string)) != terminal.DefaultReadBytes || out["has_more"] != true || out["status"] != "closed" {
		t.Fatal(out)
	}
	out = h.call(t, token, "session_read", map[string]any{"session_id": "paged", "cursor": out["next_cursor"], "max_bytes": terminal.MaxReadBytes}, false).(map[string]any)
	if len(out["output"].(string)) != terminal.MaxReadBytes || out["next_cursor"] != float64(terminal.DefaultReadBytes+terminal.MaxReadBytes) {
		t.Fatal("page options lost")
	}
	for _, limit := range []any{-1, 0, 3, terminal.MaxReadBytes + 1, 4.5, "100"} {
		h.call(t, token, "session_read", map[string]any{"session_id": "paged", "max_bytes": limit}, true)
	}
}
func TestSessionListQueriesClientAuthority(t *testing.T) {
	h := newMultiHarness(t, 0)
	token, other := h.create(t, true), h.create(t, false)
	wsA, a := h.connect(t, token, "A")
	wsB, b := h.connect(t, token, "B")
	wsOther, otherID := h.connect(t, other, "other")
	apiA, apiB := newFixtureAPI("A"), newFixtureAPI("B")
	go serveAPI(wsA, apiA)
	go serveAPI(wsB, apiB)
	go serveTestDevice(wsOther, "private", nil, nil)
	for _, id := range []string{a, b} {
		h.call(t, token, "execute_command", map[string]any{"client_id": id, "setup": map[string]any{"session_id": "same"}, "command": "first"}, false)
	}
	list := func() []any { return h.call(t, token, "session_list", nil, false).(map[string]any)["sessions"].([]any) }
	if got := list(); len(got) != 2 {
		t.Fatal(got)
	}
	h.call(t, token, "session_list", map[string]any{"client_id": otherID}, true)
	apiA.mu.Lock()
	delete(apiA.sessions, "same")
	apiA.mu.Unlock()
	got := list()
	if len(got) != 1 || got[0].(map[string]any)["client_id"] != b {
		t.Fatalf("server kept stale client state: %v", got)
	}
	wsB.Close()
	deadline := time.Now().Add(time.Second)
	for len(h.s.listClients(h.s.tokens[token])) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("disconnect not observed")
		}
		time.Sleep(time.Millisecond)
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("server invented offline session state: %v", got)
	}
}
func TestConcurrentControlCorrelationAndCancellation(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, id := h.connect(t, token, "controls")
	h.s.mu.Lock()
	c := h.s.clients[id]
	secret := h.s.tokens[token]
	h.s.mu.Unlock()
	barrier := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	go serveAPI(ws, testAPI{control: func(ctx context.Context, r terminal.ControlRequest) (terminal.ControlResult, error) {
		barrier <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return terminal.ControlResult{}, ctx.Err()
		}
		if r.Action == "eof" {
			return terminal.ControlResult{}, fmt.Errorf("EOF rejected")
		}
		return terminal.ControlResult{OK: true}, nil
	}})
	type result struct {
		action string
		err    error
	}
	results := make(chan result, 2)
	for _, action := range []string{"interrupt", "eof"} {
		go func() {
			_, err := h.s.controlSession(context.Background(), toolRequest[sessionActionArgs]{Secret: secret, Args: sessionActionArgs{ControlRequest: terminal.ControlRequest{SessionID: "terminal", Action: action}}})
			results <- result{action, err}
		}()
	}
	<-barrier
	<-barrier
	release <- struct{}{}
	release <- struct{}{}
	for range 2 {
		r := <-results
		if (r.action == "eof") != (r.err != nil) {
			t.Fatal(r)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.s.controlSession(ctx, toolRequest[sessionActionArgs]{Secret: secret, Args: sessionActionArgs{ControlRequest: terminal.ControlRequest{SessionID: "terminal", Action: "interrupt"}}})
		done <- err
	}()
	<-barrier
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatal(err)
	}
	c.mu.Lock()
	pending := len(c.pending)
	c.mu.Unlock()
	if pending != 0 {
		t.Fatal("RPC waiter leaked", pending)
	}
}
