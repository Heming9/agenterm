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
	if first["status"] != "completed" || first["command_done"] != true || first["done"] != false || first["session_alive"] != true || first["session_created"] != true || first["output"] != "first" {
		t.Fatalf("first: %v", first)
	}
	id := first["session_id"]
	h.call(t, token, "session_control", map[string]any{"session_id": id, "action": "input", "input": "echo untracked\n"}, true)
	second := h.call(t, token, "execute_command", map[string]any{"session_id": "stateful", "command": "printf '%s:%s:%s\\n' \"$keep\" \"$SAVED\" \"$INITIAL\"; pwd; cd /"}, false).(map[string]any)
	if second["session_id"] != id || second["session_created"] != false || !strings.Contains(second["output"].(string), "state:value:yes") || !strings.Contains(second["output"].(string), dir) {
		t.Fatalf("reuse: %v", second)
	}
	third := h.call(t, token, "execute_command", map[string]any{"session_id": "stateful", "command": "pwd"}, false).(map[string]any)
	if strings.TrimSpace(third["output"].(string)) != "/" {
		t.Fatalf("cd lost: %v", third)
	}
	failure := h.call(t, token, "execute_command", map[string]any{"session_id": "stateful", "command": "false"}, true).(map[string]any)
	if failure["exit_code"] != float64(1) || failure["session_alive"] != true {
		t.Fatal(failure)
	}
	h.call(t, token, "execute_command", map[string]any{"session_id": "stateful", "command": "echo no", "cwd": dir}, true)
	other := h.call(t, token, "execute_command", map[string]any{"command": "printf '%s' \"$keep\""}, false).(map[string]any)
	if other["session_id"] == id || other["output"] != "" {
		t.Fatalf("independent: %v", other)
	}
	items := h.call(t, token, "session_list", map[string]any{}, false).(map[string]any)["sessions"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["status"] != "idle" || items[0].(map[string]any)["session_id"] != "stateful" || items[1].(map[string]any)["done"] != true {
		t.Fatal(items)
	}
}

func TestServerCacheCleanupDoesNotCloseClientTerminal(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	first := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": "kept"}, "command": "echo alive"}, false).(map[string]any)
	closed := h.call(t, token, "execute_command", map[string]any{"command": "echo closed"}, false).(map[string]any)
	if closed["done"] != true {
		t.Fatalf("cleanup control was not closed: %v", closed)
	}
	h.s.mu.Lock()
	h.s.cleanupTokensLocked(time.Now().Add(31 * time.Minute))
	_, keptPresent := h.s.sessions[sessionKey(h.s.tokens[token], first["session_id"].(string))]
	_, closedPresent := h.s.sessions[sessionKey(h.s.tokens[token], closed["session_id"].(string))]
	h.s.mu.Unlock()
	if !keptPresent || closedPresent {
		t.Fatalf("cleanup retained live=%t closed=%t", keptPresent, closedPresent)
	}
	next := h.call(t, token, "execute_command", map[string]any{"session_id": "kept", "command": "echo still-alive"}, false).(map[string]any)
	if next["session_id"] != first["session_id"] || next["session_created"] != false {
		t.Fatalf("server cleanup changed terminal lifecycle: %v", next)
	}
}

func TestClientReuseIgnoresStaleServerMirror(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	first := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": "kept"}, "command": "echo default"}, false).(map[string]any)
	other := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "command": "echo other"}, false).(map[string]any)
	secret := h.s.tokens[token]
	a, _ := h.s.getSession(secret, first["session_id"].(string))
	b, _ := h.s.getSession(secret, other["session_id"].(string))
	a.mu.Lock()
	a.keepAlive = false
	a.mu.Unlock()
	b.mu.Lock()
	b.keepAlive = true
	b.mu.Unlock()
	selected := h.call(t, token, "execute_command", map[string]any{"session_id": "kept", "command": "echo actual-default"}, false).(map[string]any)
	if selected["session_id"] != first["session_id"] || selected["session_created"] != false {
		t.Fatalf("server mirror selected terminal: %v", selected)
	}
}
