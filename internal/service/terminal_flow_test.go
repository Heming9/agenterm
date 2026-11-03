package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func requirePOSIXFlow(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell commands")
	}
}

// Read each byte once and pin the command while waiting for its final page.
func readExecutionToCompletion(t *testing.T, h *multiHarness, token string, initial map[string]any) (string, map[string]any) {
	t.Helper()
	var output strings.Builder
	result := initial
	deadline := time.Now().Add(5 * time.Second)
	for {
		output.WriteString(result["output"].(string))
		if result["command_done"] == true && result["has_more"] == false {
			return output.String(), result
		}
		if time.Now().After(deadline) {
			t.Fatalf("command did not complete: %v output=%q", result, output.String())
		}
		result = h.call(t, token, "session_read", map[string]any{
			"session_id": initial["session_id"], "command_id": initial["command_id"],
			"cursor": result["next_cursor"], "wait_ms": 1000,
		}, false).(map[string]any)
		if result["command_id"] != initial["command_id"] {
			t.Fatalf("command identity changed: initial=%v result=%v", initial, result)
		}
	}
}

func TestAnonymousCommandsCloseAndReleaseSlots(t *testing.T) {
	requirePOSIXFlow(t)
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	// More than the live-terminal limit, without stop calls. A returned result
	// must already have released its slot and must not leak prior shell state.
	for i := 0; i < 12; i++ {
		command := `printf '%s' "${keep-unset}"; keep=previous`
		failed := i == 11
		if failed {
			command += "; false"
		}
		result := h.call(t, token, "execute_command", map[string]any{"command": command}, failed).(map[string]any)
		wantCode := float64(0)
		if failed {
			wantCode = 1
		}
		if result["status"] != "completed" || result["command_done"] != true || result["done"] != true || result["session_alive"] != false || result["persistent"] != false || result["promoted"] != false || result["terminal_name"] != "" || result["session_created"] != true || result["canceled"] != false || result["exit_code"] != wantCode || result["output"] != "unset" {
			t.Fatalf("iteration %d: %v", i, result)
		}
	}
	items := h.call(t, token, "session_list", map[string]any{}, false).(map[string]any)["sessions"].([]any)
	for _, item := range items {
		if item.(map[string]any)["done"] != true {
			t.Fatalf("anonymous terminal leaked: %v", item)
		}
	}
}

func TestAnonymousWaitRetainsOriginalShellAndRandomName(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	sideEffectPath := filepath.Join(t.TempDir(), "executions")
	command := "printf x >> '" + strings.ReplaceAll(sideEffectPath, "'", "'\\''") + "'; keep=retained; sleep 0.2; printf once"
	inspect := `printf '%s' "$keep"`
	if runtime.GOOS == "windows" {
		command = "[IO.File]::AppendAllText('" + strings.ReplaceAll(sideEffectPath, "'", "''") + "','x'); $keep='retained'; Start-Sleep -Milliseconds 200; [Console]::Write('once')"
		inspect = "[Console]::Write($keep)"
	}
	result := h.call(t, token, "execute_command", map[string]any{"command": command, "wait_ms": 50}, false).(map[string]any)
	name, ok := result["terminal_name"].(string)
	if !ok || !strings.HasPrefix(name, "term-") || result["persistent"] != true || result["promoted"] != true || result["session_alive"] != true || result["done"] != false || result["status"] != "running" {
		t.Fatal(result)
	}
	output, completed := readExecutionToCompletion(t, h, token, result)
	if output != "once" || completed["exit_code"] != float64(0) || completed["canceled"] != false || completed["timed_out"] != false || completed["session_alive"] != true || completed["done"] != false {
		t.Fatalf("command restarted, lost output or stopped: %v output=%q", completed, output)
	}
	executions, err := os.ReadFile(sideEffectPath)
	if err != nil || string(executions) != "x" {
		t.Fatalf("command executed more than once: side effect=%q err=%v", executions, err)
	}
	reused := h.call(t, token, "execute_command", map[string]any{"terminal_name": name, "command": inspect}, false).(map[string]any)
	if reused["session_id"] != result["session_id"] || reused["session_created"] != false || reused["output"] != "retained" || reused["persistent"] != true || reused["promoted"] != true || reused["done"] != false {
		t.Fatal(reused)
	}
	h.call(t, token, "session_control", map[string]any{"session_id": result["session_id"], "action": "stop"}, false)
}

func TestZeroWaitAnonymousIsPersistentEvenWhenCommandFinishesQuickly(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	result := h.call(t, token, "execute_command", map[string]any{"command": "echo quick", "wait_ms": 0}, false).(map[string]any)
	if result["persistent"] != true || result["promoted"] != true || result["terminal_name"] == "" || result["done"] != false {
		t.Fatal(result)
	}
	_, completed := readExecutionToCompletion(t, h, token, result)
	if completed["session_alive"] != true || completed["done"] != false || completed["exit_code"] != float64(0) {
		t.Fatal(completed)
	}
	h.call(t, token, "session_control", map[string]any{"session_id": result["session_id"], "action": "stop"}, false)
}

func TestClosedNameCreatesNewInstanceAndRejectsOldID(t *testing.T) {
	requirePOSIXFlow(t)
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	first := h.call(t, token, "execute_command", map[string]any{"terminal_name": "workspace", "command": "keep=old"}, false).(map[string]any)
	if first["terminal_name"] != "workspace" || first["persistent"] != true || first["promoted"] != false {
		t.Fatal(first)
	}
	h.call(t, token, "session_control", map[string]any{"session_id": first["session_id"], "action": "stop"}, false)
	next := h.call(t, token, "execute_command", map[string]any{"terminal_name": "workspace", "command": `printf '%s' "${keep-new}"`}, false).(map[string]any)
	if next["session_created"] != true || next["session_id"] == first["session_id"] || next["terminal_name"] != "workspace" || next["output"] != "new" {
		t.Fatal(next)
	}
	h.call(t, token, "execute_command", map[string]any{"session_id": first["session_id"], "command": "echo never"}, true)
	reused := h.call(t, token, "execute_command", map[string]any{"terminal_name": "workspace", "command": "echo alive"}, false).(map[string]any)
	if reused["session_id"] != next["session_id"] || reused["session_created"] != false {
		t.Fatal(reused)
	}
}
