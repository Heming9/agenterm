package service

import (
	"strings"
	"testing"
)

func TestSessionEOFKeepsWindowsOutputReadable(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	start := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "wait_ms": 0,
		"command": `& sort.exe; Write-Output after-eof`,
	}, false).(map[string]any)
	id := start["session_id"].(string)
	h.call(t, token, "session_control", map[string]any{"action": "interrupt", "session_id": id}, true)
	h.call(t, token, "session_control", map[string]any{"action": "input", "session_id": id, "input": "beta\r\nalpha\r\n"}, false)
	if out := h.call(t, token, "session_control", map[string]any{"action": "eof", "session_id": id}, false).(map[string]any); out["delivered"] != true {
		t.Fatal(out)
	}
	var cursor float64
	var output strings.Builder
	for {
		r := h.call(t, token, "session_read", map[string]any{"session_id": id, "cursor": cursor, "wait_ms": 1000, "max_bytes": 16}, false).(map[string]any)
		cursor = r["next_cursor"].(float64)
		output.WriteString(r["output"].(string))
		if r["done"] == true && r["has_more"] == false {
			if r["exit_code"] != float64(0) || r["canceled"] != false || r["timed_out"] != false || !strings.Contains(output.String(), "alpha\r\nbeta") || !strings.Contains(output.String(), "after-eof") {
				t.Fatalf("EOF closed output or canceled execution: %v output=%q", r, output.String())
			}
			break
		}
	}
}
