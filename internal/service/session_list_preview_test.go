package service

import (
	terminal "agenterm/internal/client"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSessionListFiltersAndBoundsLegacyClientCommands(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, clientID := h.connect(t, token, "legacy metadata")
	api := newFixtureAPI("legacy")
	longCommand := strings.Repeat("中🙂", 1000)
	for _, status := range []string{"running", "idle", "closed"} {
		api.sessions[status] = terminal.ReadOutput{SessionID: status, CommandID: "legacy-long-command-id", Status: status}
		api.commands[status] = longCommand
	}
	// This fixture returns full commands, like an older connected client.
	go serveAPI(ws, api)
	// Older callers send null arguments. Treat omission as all without applying
	// a JSON Schema property default to a nil argument map.
	all := h.call(t, token, "session_list", nil, false).(map[string]any)
	if len(all["sessions"].([]any)) != 3 {
		t.Fatal("omitted arguments must include all states", all)
	}
	for status, want := range map[string]int{"": 3, "all": 3, "live": 2, "running": 1, "idle": 1, "closed": 1} {
		args := map[string]any{"client_id": clientID}
		if status != "" {
			args["status"] = status
		}
		result := h.call(t, token, "session_list", args, false).(map[string]any)
		items := result["sessions"].([]any)
		if len(items) != want {
			t.Fatalf("filter %q: got %d want %d", status, len(items), want)
		}
		for _, item := range items {
			info := item.(map[string]any)
			command := info["command"].(string)
			if !utf8.ValidString(command) || utf8.RuneCountInString(command) != 160 || !strings.HasSuffix(command, "... (truncated)") || info["client_id"] != clientID {
				t.Fatal("unbounded or incorrectly routed preview", info)
			}
			if status == "live" && info["status"] == "closed" || status != "" && status != "all" && status != "live" && info["status"] != status {
				t.Fatal("filter returned the wrong state", info)
			}
		}
	}
	h.call(t, token, "session_list", map[string]any{"status": "invalid"}, true)
	api.mu.Lock()
	clear(api.sessions)
	api.mu.Unlock()
	result := h.call(t, token, "session_list", map[string]any{"status": "idle"}, false).(map[string]any)
	if items, ok := result["sessions"].([]any); !ok || len(items) != 0 {
		t.Fatal("empty filtered result must be an empty array", result)
	}
}
