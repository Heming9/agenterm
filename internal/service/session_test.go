package service

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"agenterm/internal/protocol"
)

func TestSessionEventsPrecedeNextAdmission(t *testing.T) {
	c := &client{secret: "owner"}
	request := &pendingSessionCommand{client: c, log: slog.Default()}
	s := &runtimeState{sessionRequests: map[string]*pendingSessionCommand{"new": request}}
	if err := s.bindAcceptedSession(c, protocol.Message{Type: "command_accepted", ID: "old", RequestID: "old", SessionID: "terminal"}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []protocol.Message{
		{Type: "output", ID: "terminal", RequestID: "old", Output: "OLD"},
		{Type: "command_done", ID: "terminal", RequestID: "old", ExitCode: 7},
	} {
		if !s.acceptSessionEvent(c, event) {
			t.Fatalf("terminal event was not applied: %v", event)
		}
	}
	if err := s.bindAcceptedSession(c, protocol.Message{Type: "command_accepted", ID: "new", RequestID: "new", SessionID: "terminal"}); err != nil {
		t.Fatal(err)
	}
	if request.cursor != 3 {
		t.Fatalf("new command cursor = %d; want 3 after previous output", request.cursor)
	}
	if s.acceptSessionEvent(&client{secret: c.secret}, protocol.Message{Type: "output", ID: "terminal", Output: "FOREIGN"}) {
		t.Fatal("another connection appended terminal output")
	}
	s.acceptSessionEvent(c, protocol.Message{Type: "output", ID: "terminal", RequestID: "new", Output: "NEW"})
	s.acceptSessionEvent(c, protocol.Message{Type: "command_done", ID: "terminal", RequestID: "new"})
	out, err := request.session.waitExecution(context.Background(), waitExecutionArgs{sessionExecution: sessionExecution{CommandID: "new", Cursor: request.cursor}})
	if err != nil {
		t.Fatal(err)
	}
	if out["output"] != "NEW" || out["exit_code"] != 0 || out["command_done"] != true {
		t.Fatalf("new command contains previous output or completion: %v", out)
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
	if reused["session_created"] != false || strings.TrimSpace(reused["output"].(string)) != "initial" {
		t.Fatalf("rejected request altered terminal: %v", reused)
	}
	h.call(t, token, "session_control", map[string]any{"session_id": first["session_id"], "action": "stop"}, false)
	closed := h.call(t, token, "session_read", map[string]any{"session_id": first["session_id"]}, false).(map[string]any)
	if closed["done"] != true {
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

func TestSessionIncrementalReadAndExit(t *testing.T) {
	s := &session{id: "test", changed: make(chan struct{})}
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.accept(protocol.Message{Type: "output", Output: "prompt> "})
	}()
	first, err := s.readCommand(context.Background(), 0, time.Second, defaultSessionReadBytes, "")
	if err != nil {
		t.Fatal(err)
	}
	if first["output"] != "prompt> " || first["done"] != false {
		t.Fatalf("unexpected first read: %#v", first)
	}
	cursor := first["next_cursor"].(int64)
	s.accept(protocol.Message{Type: "output", Output: "answer"})
	s.accept(protocol.Message{Type: "done", ExitCode: 7})
	last, err := s.readCommand(context.Background(), cursor, 0, defaultSessionReadBytes, "")
	if err != nil {
		t.Fatal(err)
	}
	if last["output"] != "answer" || last["done"] != true || last["exit_code"] != 7 {
		t.Fatalf("unexpected final read: %#v", last)
	}
}
func TestSessionBufferReportsTruncation(t *testing.T) {
	s := &session{id: "test", changed: make(chan struct{})}
	s.accept(protocol.Message{Type: "output", Output: strings.Repeat("x", maxSessionOutput+10)})
	r, err := s.readCommand(context.Background(), 0, 0, maxSessionReadBytes, "")
	if err != nil {
		t.Fatal(err)
	}
	if r["truncated"] != true || len(r["output"].(string)) != maxSessionReadBytes || r["next_cursor"] != int64(maxSessionReadBytes+10) || r["has_more"] != true {
		t.Fatalf("bad retained output metadata")
	}
	retained := len(r["output"].(string))
	for r["has_more"] == true {
		r, err = s.readCommand(context.Background(), r["next_cursor"].(int64), 0, maxSessionReadBytes, "")
		if err != nil {
			t.Fatal(err)
		}
		retained += len(r["output"].(string))
	}
	if retained != maxSessionOutput || r["next_cursor"] != int64(maxSessionOutput+10) {
		t.Fatal("lost retained output across pages")
	}
}
