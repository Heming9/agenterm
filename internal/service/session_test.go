package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"agenterm/internal/protocol"
)

func TestSessionLimitPerTokenAndFinishedReplacement(t *testing.T) {
	for _, multi := range []bool{false, true} {
		name := "single-device"
		if multi {
			name = "multi-device"
		}
		t.Run(name, func(t *testing.T) {
			h := newMultiHarness(t, 0)
			token, other := h.create(t, multi), h.create(t, false)
			ws, firstID := h.connect(t, token, "first")
			go serveTestDevice(ws, "first", nil, nil)
			ids := []string{firstID}
			if multi {
				ws, secondID := h.connect(t, token, "second")
				go serveTestDevice(ws, "second", nil, nil)
				ids = append(ids, secondID)
			}
			otherWS, _ := h.connect(t, other, "other")
			go serveTestDevice(otherWS, "other", nil, nil)
			var sessions []string
			for i := 0; i < 6; i++ {
				result := h.call(t, token, "session_start", map[string]any{
					"client_id": ids[i%len(ids)], "command": "interactive",
				}, false).(map[string]any)
				sessions = append(sessions, result["session_id"].(string))
			}
			h.call(t, token, "session_start", map[string]any{"client_id": firstID, "command": "seventh"}, true)
			h.call(t, other, "session_start", map[string]any{"command": "independent"}, false)

			h.call(t, token, "session_control", map[string]any{"action": "stop", "session_id": sessions[0]}, false)
			deadline := time.Now().Add(2 * time.Second)
			cursor := float64(0)
			for {
				result := h.call(t, token, "session_read", map[string]any{
					"session_id": sessions[0], "cursor": cursor, "wait_ms": 100,
				}, false).(map[string]any)
				if result["done"] == true {
					break
				}
				cursor = result["next_cursor"].(float64)
				if time.Now().After(deadline) {
					t.Fatal("stopped session did not finish")
				}
			}
			h.call(t, token, "session_start", map[string]any{"client_id": firstID, "command": "replacement"}, false)
			h.call(t, token, "session_read", map[string]any{"session_id": sessions[0]}, true)
			for _, id := range sessions[1:] {
				h.call(t, token, "session_read", map[string]any{"session_id": id}, false)
			}
			h.call(t, token, "session_start", map[string]any{"client_id": firstID, "command": "still full"}, true)
			h.s.mu.Lock()
			retained := len(h.s.sessions)
			h.s.mu.Unlock()
			if retained != 7 {
				t.Fatalf("retained %d session records, want 6 for one token and 1 for the other", retained)
			}
		})
	}
}

func TestConcurrentSessionStartsRespectLimit(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, _ := h.connect(t, token, "device")
	go serveTestDevice(ws, "device", nil, nil)
	var workers sync.WaitGroup
	results := make(chan bool, 12)
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, isError, err := h.request(token, "session_start", map[string]any{"command": "interactive"})
			if err != nil {
				t.Errorf("session request: %v", err)
			}
			results <- err == nil && !isError
		}()
	}
	workers.Wait()
	close(results)
	accepted := 0
	for ok := range results {
		if ok {
			accepted++
		}
	}
	if accepted != 6 {
		t.Fatalf("accepted %d concurrent sessions, want 6", accepted)
	}
}

func TestSessionIncrementalReadAndExit(t *testing.T) {
	s := &session{id: "test", changed: make(chan struct{})}
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.accept(protocol.Message{Type: "output", Output: "prompt> "})
	}()
	first, err := s.read(context.Background(), 0, time.Second, defaultSessionReadBytes)
	if err != nil {
		t.Fatal(err)
	}
	if first["output"] != "prompt> " || first["done"] != false {
		t.Fatalf("unexpected first read: %#v", first)
	}
	cursor := first["next_cursor"].(int64)
	s.accept(protocol.Message{Type: "output", Output: "answer"})
	s.accept(protocol.Message{Type: "done", ExitCode: 7})
	last, err := s.read(context.Background(), cursor, 0, defaultSessionReadBytes)
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
	r, err := s.read(context.Background(), 0, 0, maxSessionReadBytes)
	if err != nil {
		t.Fatal(err)
	}
	if r["truncated"] != true || len(r["output"].(string)) != maxSessionReadBytes || r["next_cursor"] != int64(maxSessionReadBytes+10) || r["has_more"] != true {
		t.Fatalf("bad retained output metadata")
	}
	retained := len(r["output"].(string))
	for r["has_more"] == true {
		r, err = s.read(context.Background(), r["next_cursor"].(int64), 0, maxSessionReadBytes)
		if err != nil {
			t.Fatal(err)
		}
		retained += len(r["output"].(string))
	}
	if retained != maxSessionOutput || r["next_cursor"] != int64(maxSessionOutput+10) {
		t.Fatal("lost retained output across pages")
	}
}
