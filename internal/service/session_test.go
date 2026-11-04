package service

import (
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"agenterm/internal/protocol"
)

func TestSessionAdmissionUsesClientCursor(t *testing.T) {
	c := &client{secret: "owner"}
	request := &pendingSessionCommand{client: c, log: slog.Default()}
	s := &runtimeState{sessionRequests: map[string]*pendingSessionCommand{"new": request}}
	if err := s.bindAcceptedSession(c, protocol.Message{Type: "command_accepted", ID: "new", RequestID: "new", SessionID: "terminal", SessionPersistent: true, Cursor: 42}); err != nil {
		t.Fatal(err)
	}
	if request.cursor != 42 {
		t.Fatalf("cursor=%d", request.cursor)
	}
	if s.acceptSessionEvent(&client{secret: c.secret}, protocol.Message{Type: "output", ID: "terminal", Output: "FOREIGN"}) {
		t.Fatal("foreign event accepted")
	}
}

func TestServerMirrorsClientSessionsWithoutQuota(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, true)
	for _, name := range []string{"first", "second"} {
		ws, id := h.connect(t, token, name)
		go serveTestDevice(ws, name, nil, nil)
		// This simulated client accepts 12 requests. The server mirrors its
		// decision instead of applying its own ten-terminal quota.
		for range 12 {
			h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "client_id": id, "command": "quick"}, false)
		}
	}
	h.s.mu.Lock()
	retained := len(h.s.sessions)
	h.s.mu.Unlock()
	if retained != 24 {
		t.Fatalf("server imposed its own quota: %d records", retained)
	}
}

func TestZeroWaitStillWaitsForClientAdmission(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, _ := h.connect(t, token, "admission")
	received := make(chan protocol.Message, 1)
	release := make(chan struct{})
	defer close(release)
	go func() {
		var request protocol.Message
		for {
			if ws.ReadJSON(&request) != nil {
				return
			}
			if request.Type == "session_exec" {
				break
			}
		}
		received <- request
		<-release
		_ = acceptTestSession(ws, request)
		for {
			var m protocol.Message
			if ws.ReadJSON(&m) != nil {
				return
			}
		}
	}()
	type outcome struct {
		result any
		failed bool
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, failed, err := h.request(token, "execute_command", map[string]any{"command": "waiting", "wait_ms": 0})
		finished <- outcome{result, failed, err}
	}()
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("command was not forwarded")
	}
	select {
	case result := <-finished:
		t.Fatalf("returned before client admission: %+v", result)
	case <-time.After(30 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case result := <-finished:
		if result.err != nil || result.failed || result.result.(map[string]any)["status"] != "running" {
			t.Fatalf("accepted result: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("did not return after client admission")
	}
}

func TestClosedSessionHistoryDoesNotExhaustClientSlots(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, _ := h.connect(t, token, "history")
	go serveTestDevice(ws, "history", nil, nil)
	for i := 0; i < 25; i++ {
		result := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "command": "quick"}, false).(map[string]any)
		h.call(t, token, "session_control", map[string]any{"session_id": result["session_id"], "action": "stop"}, false)
	}
	h.s.mu.Lock()
	retained := len(h.s.sessions)
	h.s.mu.Unlock()
	if retained > 11 {
		t.Fatalf("history retention is unbounded: %d", retained)
	}
}

func TestFullClientRejectsAnonymousAndMissingNamedTerminals(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	initialize, overwrite, inspect := "keep=initial", "keep=unexpected", "printf '%s' \"$keep\""
	if runtime.GOOS == "windows" {
		initialize, overwrite, inspect = "$keep='initial'", "$keep='unexpected'", "Write-Output $keep"
	}
	first := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": "original"}, "command": initialize}, false).(map[string]any)
	for range 9 {
		h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "command": "echo ready"}, false)
	}
	for _, args := range []map[string]any{
		{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "command": "echo must-not-run"},
		{"wait_ms": 0, "command": overwrite},
	} {
		denied := h.call(t, token, "execute_command", args, true).(map[string]any)
		if !strings.Contains(denied["error"].(string), "10") {
			t.Fatal(denied)
		}
	}
	reused := h.call(t, token, "execute_command", map[string]any{"session_id": first["session_id"], "command": inspect}, false).(map[string]any)
	if reused["status"] != "idle" || strings.TrimSpace(reused["output"].(string)) != "initial" {
		t.Fatalf("rejected request altered terminal: %v", reused)
	}
	h.call(t, token, "session_control", map[string]any{"session_id": first["session_id"], "action": "stop"}, false)
	closed := h.call(t, token, "session_read", map[string]any{"session_id": first["session_id"]}, false).(map[string]any)
	if closed["status"] != "closed" {
		t.Fatal(closed)
	}
	h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "command": "echo replacement"}, false)
}

func TestConcurrentSessionStartsRespectLimit(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared_name=%t", shared), func(t *testing.T) {
			h := newMultiHarness(t, 0)
			token := h.create(t, false)
			connectExecutionClient(t, h, token)
			var workers sync.WaitGroup
			results := make(chan bool, 12)
			for range 12 {
				workers.Go(func() {
					name, command := random(), "echo accepted"
					if shared {
						name, command = "shared", longCommand()
					}
					_, failed, err := h.request(token, "execute_command", map[string]any{"setup": map[string]any{"session_id": name}, "wait_ms": 0, "command": command})
					if err != nil {
						t.Error(err)
					}
					results <- err == nil && !failed
				})
			}
			workers.Wait()
			close(results)
			accepted, want := 0, 10
			if shared {
				want = 1
			}
			for ok := range results {
				if ok {
					accepted++
				}
			}
			items := h.call(t, token, "session_list", map[string]any{}, false).(map[string]any)["sessions"].([]any)
			if accepted != want || len(items) != want || (shared && items[0].(map[string]any)["session_id"] != "shared") {
				t.Fatalf("accepted=%d want=%d sessions=%v", accepted, want, items)
			}
		})
	}
}
