package service

import (
	terminal "agenterm/internal/client"

	"context"
	"testing"
)

func TestStopConfirmationAndIdempotenceThroughMCP(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	started := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": "stop"}, "command": longCommand(), "wait_ms": 0}, false).(map[string]any)
	out := h.call(t, token, "session_control", map[string]any{"session_id": started["session_id"], "action": "stop", "wait_ms": 5000}, false).(map[string]any)
	if out["status"] != "closed" || out["exit_code"] == nil {
		t.Fatal(out)
	}
	again := h.call(t, token, "session_control", map[string]any{"session_id": started["session_id"], "action": "stop"}, false).(map[string]any)
	if again["status"] != out["status"] || again["exit_code"] != out["exit_code"] {
		t.Fatal(out, again)
	}
}
func TestStopDisconnectDoesNotInventResult(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, _ := h.connect(t, token, "lost")
	go serveAPI(ws, testAPI{control: func(context.Context, terminal.ControlRequest) (terminal.ControlResult, error) {
		_ = ws.Close()
		return terminal.ControlResult{}, nil
	}})
	out := h.call(t, token, "session_control", map[string]any{"session_id": "terminal", "action": "stop"}, true).(map[string]any)
	if out["status"] != nil || out["exit_code"] != nil || out["error"] == nil {
		t.Fatalf("disconnect fabricated termination: %v", out)
	}
}
func TestStopCanceledBeforeDispatch(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, id := h.connect(t, token, "cancel")
	go serveTestDevice(ws, "cancel", nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.s.mu.Lock()
	c := h.s.clients[id]
	h.s.mu.Unlock()
	_, err := callClientAPI[terminal.ControlResult](ctx, c, "control", terminal.ControlRequest{SessionID: "terminal", Action: "stop", WaitMS: new(1000)})
	if err != context.Canceled {
		t.Fatal(err)
	}
}
