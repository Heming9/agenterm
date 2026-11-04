package service

import (
	"encoding/hex"
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
		assertCompactReadResult(t, result)
		output.WriteString(result["output"].(string))
		if result["status"] != "running" && result["has_more"] == false {
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
		if _, exists := result["session_id"]; exists {
			t.Fatalf("one-shot result included session_id: %v", result)
		}
		if result["status"] != "closed" || result["exit_code"] != wantCode || result["output"] != "unset" {
			t.Fatalf("iteration %d: %v", i, result)
		}
	}
	items := h.call(t, token, "session_list", map[string]any{}, false).(map[string]any)["sessions"].([]any)
	for _, item := range items {
		if item.(map[string]any)["status"] != "closed" {
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
	name, ok := result["session_id"].(string)
	if !ok || name == "" || result["status"] != "running" {
		t.Fatal(result)
	}
	if decoded, err := hex.DecodeString(name); err != nil || len(decoded) != 3 {
		t.Fatalf("unexpected generated session ID %q", name)
	}
	output, completed := readExecutionToCompletion(t, h, token, result)
	if completed["status"] != "idle" || output != "once" || completed["exit_code"] != float64(0) {
		t.Fatalf("command restarted, lost output or stopped: %v output=%q", completed, output)
	}
	executions, err := os.ReadFile(sideEffectPath)
	if err != nil || string(executions) != "x" {
		t.Fatalf("command executed more than once: side effect=%q err=%v", executions, err)
	}
	reused := h.call(t, token, "execute_command", map[string]any{"session_id": name, "command": inspect}, false).(map[string]any)
	if reused["status"] != "idle" || reused["session_id"] != result["session_id"] || reused["output"] != "retained" {
		t.Fatal(reused)
	}
	h.call(t, token, "session_control", map[string]any{"session_id": result["session_id"], "action": "stop"}, false)
}

func TestZeroWaitAnonymousIsPersistentEvenWhenCommandFinishesQuickly(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	result := h.call(t, token, "execute_command", map[string]any{"command": "echo quick", "wait_ms": 0}, false).(map[string]any)
	if result["session_id"] == "" || result["status"] == "closed" {
		t.Fatal(result)
	}
	_, completed := readExecutionToCompletion(t, h, token, result)
	if completed["status"] != "idle" || completed["exit_code"] != float64(0) {
		t.Fatal(completed)
	}
	h.call(t, token, "session_control", map[string]any{"session_id": result["session_id"], "action": "stop"}, false)
}

func TestSetupCreatesAndRecreatesClosedSession(t *testing.T) {
	requirePOSIXFlow(t)
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	const name = "工作区-workspace"
	first := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": name}, "command": "keep=old"}, false).(map[string]any)
	if first["status"] != "idle" || first["session_id"] != name {
		t.Fatal(first)
	}
	h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": name}, "command": "echo never"}, true)
	h.call(t, token, "session_control", map[string]any{"session_id": first["session_id"], "action": "stop"}, false)
	h.call(t, token, "execute_command", map[string]any{"session_id": name, "command": "echo never"}, true)
	next := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": name}, "command": `printf '%s' "${keep-new}"`}, false).(map[string]any)
	if next["status"] != "idle" || next["session_id"] != first["session_id"] || next["session_id"] != name || next["output"] != "new" {
		t.Fatal(next)
	}
	reused := h.call(t, token, "execute_command", map[string]any{"session_id": name, "command": "echo alive"}, false).(map[string]any)
	if reused["status"] != "idle" || reused["session_id"] != next["session_id"] {
		t.Fatal(reused)
	}
}

func TestSuppliedSessionIDIsIsolatedBetweenTokens(t *testing.T) {
	h := newMultiHarness(t, 0)
	tokens := []string{h.create(t, false), h.create(t, false)}
	for i, token := range tokens {
		label := []string{"first", "second"}[i]
		ws, _ := h.connect(t, token, label)
		go serveTestDevice(ws, label, nil, nil)
		result := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": "shared"}, "command": "quick"}, false).(map[string]any)
		if result["status"] != "idle" || result["session_id"] != "shared" || result["output"] != label+":quick" {
			t.Fatal(result)
		}
	}
	for i, token := range tokens {
		result := h.call(t, token, "session_read", map[string]any{"session_id": "shared"}, false).(map[string]any)
		if result["output"] != []string{"first:quick", "second:quick"}[i] {
			t.Fatal(result)
		}
	}
}

func TestReuseUnknownSessionDoesNotCreateTerminal(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	h.call(t, token, "execute_command", map[string]any{"session_id": "missing", "command": "echo never", "wait_ms": 0}, true)
	items := h.call(t, token, "session_list", map[string]any{}, false).(map[string]any)["sessions"].([]any)
	if len(items) != 0 {
		t.Fatalf("reuse created a terminal: %v", items)
	}
}

func TestSetupWithoutIDUsesAnonymousLifetimeAndInitialOptions(t *testing.T) {
	for _, wait := range []int{10000, 0} {
		t.Run(map[bool]string{true: "immediate-retention", false: "one-shot"}[wait == 0], func(t *testing.T) {
			h := newMultiHarness(t, 0)
			token := h.create(t, false)
			connectExecutionClient(t, h, token)
			dir := t.TempDir()
			command := `printf '%s\n' "$SETUP_TEST_VALUE"; pwd`
			if runtime.GOOS == "windows" {
				command = "Write-Output $env:SETUP_TEST_VALUE; Get-Location"
			}
			started := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"cwd": dir, "env": map[string]string{"SETUP_TEST_VALUE": "initialized"}}, "command": command, "wait_ms": wait}, false).(map[string]any)
			if _, exists := started["session_id"]; exists != (wait == 0) {
				t.Fatal(started)
			}
			output, completed := started["output"].(string), started
			if wait == 0 {
				output, completed = readExecutionToCompletion(t, h, token, started)
			}
			if !strings.Contains(output, "initialized") || !strings.Contains(output, dir) {
				t.Fatalf("setup lost: %v output=%q", completed, output)
			}
			retained := wait == 0
			wantStatus := "closed"
			if retained {
				wantStatus = "idle"
			}
			if completed["status"] != wantStatus {
				t.Fatal(completed)
			}
			if retained {
				h.call(t, token, "session_control", map[string]any{"session_id": started["session_id"], "action": "stop"}, false)
			}
		})
	}
}

func TestAnonymousLargeOutputComesFromClient(t *testing.T) {
	requirePOSIXFlow(t)
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	const size = 17 << 20
	result, failed, err := h.request(token, "execute_command", map[string]any{"command": `head -c 17825792 /dev/zero | tr '\000' x`}, 20*time.Second)
	if err != nil || failed {
		t.Fatalf("large execution: failed=%t err=%v", failed, err)
	}
	out := result.(map[string]any)
	if len(out["output"].(string)) != defaultSessionReadBytes || out["session_id"] == nil || out["has_more"] != true || out["status"] != "closed" {
		t.Fatalf("expected completed paged output: size=%d id=%v more=%v status=%v", len(out["output"].(string)), out["session_id"], out["has_more"], out["status"])
	}
	var output strings.Builder
	output.WriteString(out["output"].(string))
	initial := out
	for out["has_more"] == true {
		out = h.call(t, token, "session_read", map[string]any{"session_id": initial["session_id"], "command_id": initial["command_id"], "cursor": out["next_cursor"], "max_bytes": maxSessionReadBytes}, false).(map[string]any)
		if out["truncated"] != nil {
			t.Fatal("lost anonymous output while paging")
		}
		output.WriteString(out["output"].(string))
	}
	text := output.String()
	if len(text) != size || strings.Trim(text, "x") != "" || out["exit_code"] != float64(0) {
		t.Fatalf("incomplete output: size=%d exit=%v", len(text), out["exit_code"])
	}
	h.call(t, token, "execute_command", map[string]any{"session_id": initial["session_id"], "command": "echo must-not-run"}, true)
}
