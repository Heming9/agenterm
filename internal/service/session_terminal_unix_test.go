//go:build linux || darwin

package service

import (
	"strings"
	"testing"
	"time"
)

// Exercise MCP -> runtime -> WebSocket -> real PTY, including the foreground
// process group rather than only testing an injected Ctrl+C character.
func TestSessionInterruptAndEOFKeepShellState(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	start := h.call(t, token, "session_start", map[string]any{
		"command": "/bin/sh -i", "env": map[string]string{"PS1": "", "PS2": ""}, "timeout_ms": 15000,
	}, false).(map[string]any)
	id := start["session_id"].(string)
	var cursor float64
	write := func(input string) {
		t.Helper()
		h.call(t, token, "session_control", map[string]any{"action": "input", "session_id": id, "input": input}, false)
	}
	wait := func(marker string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		var output strings.Builder
		for time.Now().Before(deadline) {
			r := h.call(t, token, "session_read", map[string]any{"session_id": id, "cursor": cursor, "wait_ms": 500, "max_bytes": 256}, false).(map[string]any)
			cursor = r["next_cursor"].(float64)
			output.WriteString(r["output"].(string))
			if strings.Contains(output.String(), marker) {
				if r["done"] != false {
					t.Fatalf("shell ended while waiting for %s: %v", marker, r)
				}
				return
			}
			if r["done"] == true {
				t.Fatalf("shell ended: %v output=%q", r, output.String())
			}
		}
		t.Fatalf("missing %s: %q", marker, output.String())
	}
	write("stty -echo; keep=state; printf '__%s__\\n' READY\n")
	wait("__READY__")
	write("sh -c 'printf \"__%s__\\n\" BUSY; exec sleep 30'\n")
	wait("__BUSY__")
	interrupted := h.call(t, token, "session_control", map[string]any{"action": "interrupt", "session_id": id}, false).(map[string]any)
	if interrupted["delivered"] != true {
		t.Fatal(interrupted)
	}
	write("printf '__%s__:%s\\n' STILL \"$keep\"\n")
	wait("__STILL__:state")
	write("stty eof '^A'; printf '__%s__\\n' CAT; cat; printf '__%s__:%s\\n' EOF \"$keep\"\n")
	wait("__CAT__")
	// EOF must follow queued input and use the terminal's configured VEOF.
	write("queued-input")
	if eof := h.call(t, token, "session_control", map[string]any{"action": "eof", "session_id": id}, false).(map[string]any); eof["delivered"] != true {
		t.Fatal(eof)
	}
	wait("queued-input")
	// The first EOF flushes an unfinished input line; the next yields EOF.
	h.call(t, token, "session_control", map[string]any{"action": "eof", "session_id": id}, false)
	wait("__EOF__:state")
	write("stty eof undef; printf '__%s__\\n' DISABLED\n")
	wait("__DISABLED__")
	h.call(t, token, "session_control", map[string]any{"action": "eof", "session_id": id}, true)
	write("stty eof '^D'; printf '__%s__\\n' ENABLED\n")
	wait("__ENABLED__")
	write("stty -icanon -echo; printf '__%s__\\n' RAW; sleep 30; stty icanon\n")
	wait("__RAW__")
	h.call(t, token, "session_control", map[string]any{"action": "eof", "session_id": id}, true)
	h.call(t, token, "session_control", map[string]any{"action": "interrupt", "session_id": id}, false)
	write("printf '__%s__:%s\\n' RESTORED \"$keep\"\n")
	wait("__RESTORED__:state")
	h.call(t, token, "session_control", map[string]any{"action": "stop", "session_id": id}, false)
}

func TestSessionInterruptWhileStdinWriteIsBlocked(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	id := h.call(t, token, "session_start", map[string]any{
		"command": "stty raw -echo; printf '__%s__\\n' BLOCKED; sleep 30", "timeout_ms": 10000,
	}, false).(map[string]any)["session_id"].(string)
	var output strings.Builder
	var cursor float64
	for !strings.Contains(output.String(), "__BLOCKED__") {
		r := h.call(t, token, "session_read", map[string]any{"session_id": id, "cursor": cursor, "wait_ms": 1000}, false).(map[string]any)
		cursor = r["next_cursor"].(float64)
		output.WriteString(r["output"].(string))
		if r["done"] == true {
			t.Fatalf("process ended before readiness: %v", r)
		}
	}
	h.call(t, token, "session_control", map[string]any{"action": "input", "session_id": id, "input": strings.Repeat("x", 256<<10)}, false)
	// A blocking PTY write must not prevent the reader from receiving SIGINT.
	h.call(t, token, "session_control", map[string]any{"action": "interrupt", "session_id": id}, false)
	h.call(t, token, "session_control", map[string]any{"action": "stop", "session_id": id}, false)
	// The same connection remains usable and cleanup does not wait for sleep.
	h.call(t, token, "execute_command", map[string]any{"command": "printf connection-alive"}, false)
}
