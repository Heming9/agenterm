package service

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

func TestPersistentProtocolCapability(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		accepted     bool
	}{
		{name: "missing"},
		{name: "legacy", header: "1"},
		{name: "current", header: protocol.PersistentSessionsVersion, accepted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newMultiHarness(t, 0)
			token := h.create(t, false)
			decoded, _ := protocol.Decode(token)
			headers := http.Header{"Authorization": {"Bearer " + decoded.Secret}, version.Header: {version.Current}}
			if tc.header != "" {
				headers.Set(protocol.PersistentSessionsHeader, tc.header)
			}
			ws, _, err := websocket.DefaultDialer.Dial(decoded.ConnectionURL(), headers)
			if err != nil {
				t.Fatal(err)
			}
			defer ws.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := h.s.waitForClients(ctx, decoded.Secret, time.Second); err != nil {
				t.Fatal(err)
			}
			if tc.accepted {
				go serveTestDevice(ws, "compatible", nil, nil)
			}
			out := h.call(t, token, "execute_command", map[string]any{"command": "probe", "wait_ms": 0}, !tc.accepted).(map[string]any)
			h.s.mu.Lock()
			count := len(h.s.sessions)
			h.s.mu.Unlock()
			if tc.accepted {
				if count != 1 || out["session_id"] == "" {
					t.Fatalf("compatible client rejected: sessions=%d result=%v", count, out)
				}
			} else {
				if count != 0 {
					t.Fatalf("unsupported client acquired %d sessions", count)
				}
				_ = ws.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				for {
					var message protocol.Message
					if err := ws.ReadJSON(&message); err != nil {
						if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
							t.Fatal(err)
						}
						break
					}
					if message.Type != "latency" {
						t.Fatalf("dispatched to unsupported client: %+v", message)
					}
				}
			}
		})
	}
}

func TestSessionReadUTF8PaginationAfterExit(t *testing.T) {
	p := &session{id: "utf8", changed: make(chan struct{})}
	want := "abc你好🙂xyz"
	p.accept(protocol.Message{Type: "output", Output: want})
	p.accept(protocol.Message{Type: "done", ExitCode: 7})
	var got strings.Builder
	var cursor int64
	for {
		r, err := p.readCommand(context.Background(), cursor, 0, 4, "")
		if err != nil {
			t.Fatal(err)
		}
		output := r["output"].(string)
		if len(output) > 4 || !utf8.ValidString(output) || r["status"] != "closed" || r["exit_code"] != 7 || r["truncated"] != nil {
			t.Fatalf("invalid page: %v", r)
		}
		next := r["next_cursor"].(int64)
		if next != cursor+int64(len(output)) || next <= cursor {
			t.Fatalf("cursor did not advance by returned bytes: %v", r)
		}
		got.WriteString(output)
		cursor = next
		if r["has_more"] == false {
			break
		}
	}
	if got.String() != want || cursor != int64(len(want)) {
		t.Fatalf("lost output: %q", got.String())
	}
	if _, err := p.readCommand(context.Background(), 4, 0, 4, ""); err == nil {
		t.Fatal("accepted a cursor inside a multibyte character")
	}
	for _, limit := range []int{0, 1, 3, maxSessionReadBytes + 1} {
		if _, err := p.readCommand(context.Background(), 0, 0, limit, ""); err == nil {
			t.Fatalf("accepted invalid limit %d", limit)
		}
	}
	if _, err := p.readCommand(context.Background(), -1, 0, 4, ""); err == nil {
		t.Fatal("accepted negative cursor")
	}
	if _, err := p.readCommand(context.Background(), cursor+1, 0, 4, ""); err == nil {
		t.Fatal("accepted cursor beyond output")
	}
}

func TestSessionTruncationPreservesUTF8Boundary(t *testing.T) {
	p := &session{id: "utf8", changed: make(chan struct{})}
	p.accept(protocol.Message{Type: "output", Output: strings.Repeat("你", maxSessionOutput/3+2)})
	r, err := p.readCommand(context.Background(), 0, 0, 4, "")
	if err != nil || r["truncated"] != true || r["output"] != "你" || !utf8.Valid(p.output) || p.offset%3 != 0 {
		t.Fatalf("invalid truncated page: %v err=%v offset=%d", r, err, p.offset)
	}
}

func TestSessionReadLimitThroughMCP(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	decoded, _ := protocol.Decode(token)
	p := &session{id: "paged", owner: decoded.Secret, changed: make(chan struct{})}
	p.accept(protocol.Message{Type: "output", Output: strings.Repeat("x", 2*maxSessionReadBytes)})
	p.accept(protocol.Message{Type: "done", ExitCode: 0})
	h.s.mu.Lock()
	h.s.sessions = map[string]*session{sessionKey(p.owner, p.id): p}
	h.s.mu.Unlock()
	r := h.call(t, token, "session_read", map[string]any{"session_id": p.id}, false).(map[string]any)
	if len(r["output"].(string)) != defaultSessionReadBytes || r["has_more"] != true || r["status"] != "closed" {
		t.Fatalf("default paging: %v", r)
	}
	r = h.call(t, token, "session_read", map[string]any{"session_id": p.id, "cursor": r["next_cursor"], "max_bytes": maxSessionReadBytes}, false).(map[string]any)
	if len(r["output"].(string)) != maxSessionReadBytes || r["next_cursor"] != float64(defaultSessionReadBytes+maxSessionReadBytes) || r["truncated"] != nil {
		t.Fatal("explicit read limit not applied")
	}
	for _, limit := range []any{-1, 0, 3, maxSessionReadBytes + 1, 4.5, "100"} {
		h.call(t, token, "session_read", map[string]any{"session_id": p.id, "max_bytes": limit}, true)
	}
}

func TestSessionListIsolationFilterAndDisconnectedHistory(t *testing.T) {
	h := newMultiHarness(t, 0)
	token, other := h.create(t, true), h.create(t, false)
	wsA, a := h.connect(t, token, "A")
	wsB, b := h.connect(t, token, "B")
	wsOther, otherID := h.connect(t, other, "other")
	go serveTestDevice(wsA, "A", nil, nil)
	go serveTestDevice(wsB, "B", nil, nil)
	go serveTestDevice(wsOther, "other", nil, nil)
	start := func(auth, clientID, command string) string {
		return h.call(t, auth, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "client_id": clientID, "command": command}, false).(map[string]any)["session_id"].(string)
	}
	idA := start(token, a, "first")
	idB := start(token, b, "second")
	start(other, otherID, "private-command")
	for _, id := range []string{idA, idB} {
		h.call(t, token, "session_read", map[string]any{"session_id": id, "wait_ms": 1000}, false)
	}
	list := func(auth string, args map[string]any) []any {
		return h.call(t, auth, "session_list", args, false).(map[string]any)["sessions"].([]any)
	}
	items := list(token, map[string]any{})
	if len(items) != 2 || items[0].(map[string]any)["session_id"] != idA || items[1].(map[string]any)["session_id"] != idB {
		t.Fatalf("list order/isolation: %v", items)
	}
	info := items[0].(map[string]any)
	if info["command"] != "first" || info["status"] != "idle" || info["command_id"] == "" || info["exit_code"] != float64(0) {
		t.Fatalf("missing session metadata: %v", info)
	}
	if len(list(token, map[string]any{"client_id": b})) != 1 || len(list(token, map[string]any{"client_id": otherID})) != 0 || len(list(other, map[string]any{})) != 1 {
		t.Fatal("device/token filter leaked sessions")
	}
	h.call(t, token, "session_control", map[string]any{"action": "interrupt", "session_id": idA}, false)
	h.call(t, token, "session_control", map[string]any{"action": "eof", "session_id": idA}, false)
	for _, action := range []string{"interrupt", "eof"} {
		h.call(t, other, "session_control", map[string]any{"action": action, "session_id": idA}, true)
		h.call(t, token, "session_control", map[string]any{"action": action, "session_id": "unknown"}, true)
	}
	wsA.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		item := list(token, map[string]any{"client_id": a})[0].(map[string]any)
		if item["status"] == "closed" {
			if item["status"] != "closed" || item["exit_code"] != float64(-1) || item["error"] == "" {
				t.Fatalf("lost disconnected history: %v", item)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnected session never finished")
		}
		time.Sleep(time.Millisecond)
	}
	for _, action := range []string{"interrupt", "eof"} {
		h.call(t, token, "session_control", map[string]any{"action": action, "session_id": idA}, true)
	}
	decoded, _ := protocol.Decode(token)
	h.s.mu.Lock()
	h.s.sessions[sessionKey(h.s.tokens[token], idA)].mu.Lock()
	h.s.sessions[sessionKey(h.s.tokens[token], idA)].ended = time.Now().Add(-31 * time.Minute)
	h.s.sessions[sessionKey(h.s.tokens[token], idA)].mu.Unlock()
	h.s.mu.Unlock()
	if len(h.s.listSessions(decoded.Secret, "")) != 1 {
		t.Fatal("expired finished session remained visible")
	}
}

func TestSessionControlAcknowledgmentAndErrors(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, _ := h.connect(t, token, "controls")
	// EOF returns a real client error; interrupt is left unacknowledged.
	go func() {
		for {
			var m protocol.Message
			if ws.ReadJSON(&m) != nil {
				return
			}
			if m.Type == "session_exec" {
				m = acceptTestSession(ws, m)
			}
			if m.Type == "eof" {
				_ = ws.WriteJSON(protocol.Message{Type: "control_done", ID: m.RequestID, Error: "raw terminal rejects EOF"})
			}
		}
	}()
	id := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "command": "test"}, false).(map[string]any)["session_id"].(string)
	r := h.call(t, token, "session_control", map[string]any{"action": "eof", "session_id": id}, true).(map[string]any)
	if !strings.Contains(r["error"].(string), "raw terminal") {
		t.Fatalf("client failure was lost: %v", r)
	}
	decoded, _ := protocol.Decode(token)
	p, err := h.s.getSession(decoded.Secret, id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if out, err := p.requestControl(ctx, "interrupt", ""); err == nil || out.OK {
		t.Fatal("unacknowledged signal reported as delivered")
	}
	p.client.mu.Lock()
	count := len(p.client.pending)
	p.client.mu.Unlock()
	if count != 0 {
		t.Fatalf("control waiter leaked: %d", count)
	}
}

func TestSessionControlsRequireAdvertisedCapabilities(t *testing.T) {
	for _, capabilities := range []string{"", "eof"} {
		t.Run("capabilities="+capabilities, func(t *testing.T) {
			h := newMultiHarness(t, 0)
			token := h.create(t, false)
			decoded, _ := protocol.Decode(token)
			ws, _, err := websocket.DefaultDialer.Dial(decoded.ConnectionURL(), http.Header{
				"Authorization": {"Bearer " + decoded.Secret}, version.Header: {version.Current},
				protocol.SessionControlsHeader: {capabilities}, protocol.PersistentSessionsHeader: {protocol.PersistentSessionsVersion}, protocol.PlatformHeader: {"windows"},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer ws.Close()
			go serveTestDevice(ws, "capabilities", nil, nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			clients, err := h.s.waitForClients(ctx, decoded.Secret, time.Second)
			if err != nil || len(clients) != 1 || len(clients[0].Environment.SessionControls) != map[string]int{"": 0, "eof": 1}[capabilities] {
				t.Fatalf("capability discovery: %v %v", clients, err)
			}
			id := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "command": "test"}, false).(map[string]any)["session_id"].(string)
			h.call(t, token, "session_control", map[string]any{"action": "interrupt", "session_id": id}, true)
			h.call(t, token, "session_control", map[string]any{"action": "eof", "session_id": id}, capabilities == "")
		})
	}
}

func TestConcurrentSessionControlsMatchTheirAcknowledgments(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, _ := h.connect(t, token, "concurrent")
	defer ws.Close()
	go func() {
		requests := make([]protocol.Message, 0, 2)
		for len(requests) < 2 {
			var m protocol.Message
			if ws.ReadJSON(&m) != nil {
				return
			}
			if m.Type == "session_exec" {
				m = acceptTestSession(ws, m)
			}
			if m.Type == "interrupt" || m.Type == "eof" {
				requests = append(requests, m)
			}
		}
		for i := len(requests) - 1; i >= 0; i-- {
			m := requests[i]
			reply := protocol.Message{Type: "control_done", ID: m.RequestID}
			if m.Type == "eof" {
				reply.Error = "EOF rejected"
			}
			_ = ws.WriteJSON(reply)
		}
	}()
	id := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "command": "test"}, false).(map[string]any)["session_id"].(string)
	decoded, _ := protocol.Decode(token)
	p, err := h.s.getSession(decoded.Secret, id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type result struct {
		action string
		out    sessionControlResult
		err    error
	}
	results := make(chan result, 2)
	for _, action := range []string{"interrupt", "eof"} {
		go func() {
			out, err := p.requestControl(ctx, action, "")
			results <- result{action, out, err}
		}()
	}
	for range 2 {
		r := <-results
		if r.action == "interrupt" && (r.err != nil || !r.out.OK) {
			t.Fatalf("interrupt got wrong acknowledgment: %+v", r)
		}
		if r.action == "eof" && (r.err == nil || !strings.Contains(r.err.Error(), "EOF rejected") || r.out.OK) {
			t.Fatalf("EOF got wrong acknowledgment: %+v", r)
		}
	}
}
