//go:build linux || darwin

package service

import (
	"strings"
	"testing"
)

func TestPersistentForegroundInterruptKeepsNamedShell(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	first := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": "interrupt"}, "command": "keep=preserved; printf saved"}, false).(map[string]any)
	active := h.call(t, token, "execute_command", map[string]any{"session_id": "interrupt", "command": "printf ready; sleep 30", "wait_ms": 100}, false).(map[string]any)
	if active["status"] != "running" || active["session_id"] != first["session_id"] {
		t.Fatal(active)
	}
	id := active["session_id"]
	h.call(t, token, "session_control", map[string]any{"session_id": id, "action": "interrupt"}, false)
	_, completed := readExecutionToCompletion(t, h, token, active)
	if completed["session_alive"] != true || completed["exit_code"] == float64(0) {
		t.Fatal(completed)
	}
	last := h.call(t, token, "execute_command", map[string]any{"session_id": "interrupt", "command": "printf '%s' \"$keep\""}, false).(map[string]any)
	if last["session_id"] != id || last["session_created"] != false || !strings.Contains(last["output"].(string), "preserved") {
		t.Fatal(last)
	}
}

func TestNestedShellPromptDoesNotCompleteParentCommand(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	active := h.call(t, token, "execute_command", map[string]any{"command": "/bin/sh -i", "wait_ms": 100}, false).(map[string]any)
	if active["command_done"] != false {
		t.Fatalf("nested prompt completed parent: %v", active)
	}
	id := active["session_id"]
	h.call(t, token, "session_control", map[string]any{"session_id": id, "action": "input", "input": "printf nested; exit\n"}, false)
	output, completed := readExecutionToCompletion(t, h, token, active)
	if completed["session_alive"] != true || !strings.Contains(output, "nested") {
		t.Fatalf("nested completion: %v output=%q", completed, output)
	}
}
