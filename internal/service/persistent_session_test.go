package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNamedSessionStateAndFastCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX commands; Windows coverage uses PowerShell")
	}
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	dir := filepath.Join(t.TempDir(), "space ' unicode 你好")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	first := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": "stateful", "cwd": dir, "env": map[string]string{"INITIAL": "yes"}}, "command": "keep=state; export SAVED=value; printf first"}, false).(map[string]any)
	if first["status"] != "idle" || first["output"] != "first" {
		t.Fatalf("first: %v", first)
	}
	id := first["session_id"]
	h.call(t, token, "session_control", map[string]any{"session_id": id, "action": "input", "input": "echo untracked\n"}, true)
	second := h.call(t, token, "execute_command", map[string]any{"session_id": "stateful", "command": "printf '%s:%s:%s\\n' \"$keep\" \"$SAVED\" \"$INITIAL\"; pwd; cd /"}, false).(map[string]any)
	if second["session_id"] != id || second["status"] != "idle" || !strings.Contains(second["output"].(string), "state:value:yes") || !strings.Contains(second["output"].(string), dir) {
		t.Fatalf("reuse: %v", second)
	}
	third := h.call(t, token, "execute_command", map[string]any{"session_id": "stateful", "command": "pwd"}, false).(map[string]any)
	if strings.TrimSpace(third["output"].(string)) != "/" {
		t.Fatalf("cd lost: %v", third)
	}
	failure := h.call(t, token, "execute_command", map[string]any{"session_id": "stateful", "command": "false"}, true).(map[string]any)
	if failure["exit_code"] != float64(1) || failure["status"] != "idle" {
		t.Fatal(failure)
	}
	h.call(t, token, "execute_command", map[string]any{"session_id": "stateful", "command": "echo no", "cwd": dir}, true)
	other := h.call(t, token, "execute_command", map[string]any{"command": "printf '%s' \"$keep\""}, false).(map[string]any)
	if other["session_id"] == id || other["output"] != "" {
		t.Fatalf("independent: %v", other)
	}
	items := h.call(t, token, "session_list", map[string]any{}, false).(map[string]any)["sessions"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["status"] != "idle" || items[0].(map[string]any)["session_id"] != "stateful" || items[1].(map[string]any)["status"] != "closed" {
		t.Fatal(items)
	}
}

func TestTokenCleanupDoesNotOwnTerminalHistory(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	started := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": "kept"}, "command": "echo kept"}, false).(map[string]any)
	h.s.mu.Lock()
	h.s.cleanupTokensLocked(time.Now().Add(48 * time.Hour))
	h.s.mu.Unlock()
	out := h.call(t, token, "session_read", map[string]any{"session_id": started["session_id"]}, false).(map[string]any)
	if !strings.Contains(out["output"].(string), "kept") {
		t.Fatal(out)
	}
}
