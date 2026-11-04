package service

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSessionControlDiscoveryAndRetiredTools(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, auth := range []string{"admin-test", token} {
		client := connectMCPClient(t, h, auth)
		listed, err := client.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"close_client", "execute_command", "list_client", "p2p_transfer_start", "session_control", "session_list", "session_read"}
		if auth == "admin-test" {
			want = append(want, "create_client")
		}
		slices.Sort(want)
		got := make([]string, 0, len(listed.Tools))
		for _, tool := range listed.Tools {
			got = append(got, tool.Name)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("tools for %q: got %v want %v", auth, got, want)
		}
		for _, name := range []string{"session_start", "session_write", "session_interrupt", "session_eof", "session_stop"} {
			args := map[string]any{"session_id": "unknown", "input": "text"}
			if auth == "admin-test" {
				args["client_token"] = token
			}
			result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
			if err == nil || !strings.Contains(err.Error(), "unknown tool") {
				t.Fatalf("retired tool %s was not rejected as unknown: %v %v", name, result, err)
			}
		}
	}
}

func TestSessionControlResultSchemaSeparatesAcknowledgmentAndStop(t *testing.T) {
	schema, err := sessionActionResultSchema().Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []map[string]any{
		{"ok": true},
		{"status": "running"},
		{"status": "closed", "exit_code": 0},
		{"status": "closed", "error": "connection lost; remote termination unconfirmed"},
	} {
		if err := schema.Validate(result); err != nil {
			t.Fatalf("valid result rejected: %v %v", result, err)
		}
	}
	for _, result := range []map[string]any{
		{}, {"ok": false}, {"accepted": true}, {"delivered": true},
		{"action": "input", "ok": true},
		{"status": "idle"}, {"status": "finished"},
		{"ok": true, "status": "closed"},
		{"status": "closed", "done": true},
	} {
		if err := schema.Validate(result); err == nil {
			t.Fatalf("redundant or mixed result accepted: %v", result)
		}
	}
}
