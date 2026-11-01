package main

import (
	"agenterm/internal/protocol"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestStopConfirmationAndIdempotenceThroughMCP(t *testing.T) {
	h := newMultiHarness(t, 0)
	token, other := h.create(t, false), h.create(t, false)
	ws, _ := h.connect(t, token, "controlled stop")
	cancels := make(chan string, 8)
	finish := make(chan protocol.Message, 1)
	var writes sync.Mutex
	go func() {
		for {
			var message protocol.Message
			if ws.ReadJSON(&message) != nil {
				return
			}
			if message.Type == "cancel" {
				cancels <- message.ID
			}
		}
	}()
	go func() {
		for message := range finish {
			writes.Lock()
			_ = ws.WriteJSON(message)
			writes.Unlock()
		}
	}()
	defer close(finish)
	started := h.call(t, token, "session_start", map[string]any{"command": "interactive"}, false).(map[string]any)
	id := started["session_id"].(string)
	h.call(t, other, "session_stop", map[string]any{"session_id": id}, true)
	h.call(t, token, "session_stop", map[string]any{"session_id": "unknown"}, true)
	for _, wait := range []any{-1, 20001, 1.5} {
		h.call(t, token, "session_stop", map[string]any{"session_id": id, "wait_ms": wait}, true)
	}
	first := h.call(t, token, "session_stop", map[string]any{"session_id": id, "wait_ms": 0}, false).(map[string]any)
	if first["accepted"] != true || first["done"] != false || first["status"] != "stopping" || first["already_done"] != false || first["next_action"] == "" {
		t.Fatalf("unconfirmed cancellation: %v", first)
	}
	for _, field := range []string{"exit_code", "canceled", "timed_out"} {
		if _, exists := first[field]; exists {
			t.Fatalf("unconfirmed stop invented terminal field %s", field)
		}
	}
	select {
	case got := <-cancels:
		if got != id {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("stop request did not reach the target")
	}
	// A pending request must not be sent twice, including while waiting for its
	// terminal acknowledgement. The bounded wait must not invent completion.
	again := h.call(t, token, "session_stop", map[string]any{"session_id": id, "wait_ms": 20}, false).(map[string]any)
	if again["status"] != "stopping" || again["done"] != false {
		t.Fatal(again)
	}
	completed := make(chan map[string]any, 1)
	errors := make(chan error, 1)
	go func() {
		// Omitted wait_ms uses the advertised 1000 ms confirmation window.
		result, failed, err := h.request(token, "session_stop", map[string]any{"session_id": id})
		if err != nil || failed {
			errors <- err
			return
		}
		completed <- result.(map[string]any)
	}()
	select {
	case result := <-completed:
		t.Fatalf("default wait returned before target confirmation: %v", result)
	case err := <-errors:
		t.Fatalf("stop failed: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	finish <- protocol.Message{Type: "done", ID: id, ExitCode: -1, Canceled: true}
	select {
	case result := <-completed:
		if result["status"] != "stopped" || result["done"] != true || result["canceled"] != true || result["accepted"] != true || result["exit_code"] != float64(-1) {
			t.Fatalf("confirmed cancellation: %v", result)
		}
	case err := <-errors:
		t.Fatalf("stop failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not observe the terminal acknowledgement")
	}
	ended := h.call(t, token, "session_stop", map[string]any{"session_id": id}, false).(map[string]any)
	if ended["already_done"] != true || ended["accepted"] != false || ended["done"] != true || ended["status"] != "stopped" {
		t.Fatalf("repeat after completion: %v", ended)
	}
	select {
	case duplicate := <-cancels:
		t.Fatalf("duplicate cancellation request: %s", duplicate)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestStopDoesNotMisreportOtherTerminalStates(t *testing.T) {
	for _, test := range []struct {
		name    string
		message protocol.Message
	}{
		{"normal exit", protocol.Message{Type: "done", ExitCode: 0}},
		{"timeout", protocol.Message{Type: "done", ExitCode: -1, TimedOut: true}},
		{"disconnect", protocol.Message{Type: "done", ExitCode: -1, Error: "client disconnected"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &session{id: "finished", changed: make(chan struct{})}
			p.accept(test.message)
			out, err := p.stop(context.Background(), time.Second)
			if err != nil || out.Status != "finished" || !out.Done || !out.AlreadyDone || out.Accepted || out.Canceled == nil || *out.Canceled || *out.ExitCode != test.message.ExitCode || out.Error != test.message.Error {
				t.Fatalf("terminal state: %+v err=%v", out, err)
			}
		})
	}
}

func TestStopRespectsCanceledRequestBeforeSending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A missing device connection would panic if cancellation were attempted.
	p := &session{id: "untouched", changed: make(chan struct{})}
	if _, err := p.stop(ctx, time.Second); !errors.Is(err, context.Canceled) || p.stopRequested {
		t.Fatalf("canceled request had effects: %v", err)
	}
}

func TestStopDisconnectDoesNotConfirmCancellation(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, _ := h.connect(t, token, "disconnect during stop")
	go func() {
		for {
			var message protocol.Message
			if ws.ReadJSON(&message) != nil {
				return
			}
			if message.Type == "cancel" {
				ws.Close()
				return
			}
		}
	}()
	started := h.call(t, token, "session_start", map[string]any{"command": "interactive"}, false).(map[string]any)
	out := h.call(t, token, "session_stop", map[string]any{"session_id": started["session_id"]}, false).(map[string]any)
	if out["status"] != "finished" || out["done"] != true || out["canceled"] != false || out["accepted"] != true || out["error"] == nil || out["error"] == "" {
		t.Fatalf("disconnect was confused with confirmed cancellation: %v", out)
	}
}
