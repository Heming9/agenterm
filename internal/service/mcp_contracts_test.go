package service

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAllToolResultsMatchPublishedContracts(t *testing.T) {
	p2pTestAssets(t)
	h := newMultiHarness(t, 0)
	admin := connectMCPClient(t, h, "admin-test")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listed, err := admin.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := make(map[string]*jsonschema.Resolved)
	for _, tool := range listed.Tools {
		data, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(data, &schema); err != nil || len(schema.Properties) == 0 {
			t.Fatalf("%s has no concrete result schema: %s %v", tool.Name, data, err)
		}
		if tool.Name == "execute_command" || tool.Name == "session_read" {
			expected := []string{"session_id", "command_id", "status", "output", "next_cursor", "has_more"}
			if len(schema.Properties) != 9 || len(schema.Required) != len(expected) {
				t.Fatalf("%s result is not compact: %s", tool.Name, data)
			}
			for _, field := range expected {
				found := false
				for _, required := range schema.Required {
					if field == required {
						found = true
					}
				}
				if !found {
					t.Fatalf("%s missing required %s", tool.Name, field)
				}
			}
			if !reflect.DeepEqual(schema.Properties["status"].Enum, []any{"running", "idle", "closed"}) {
				t.Fatalf("%s has wrong states", tool.Name)
			}
			if !reflect.DeepEqual(schema.Properties["truncated"].Enum, []any{true}) {
				t.Fatalf("%s permits a redundant false truncation field", tool.Name)
			}
		}
		var checkDescriptions func(*jsonschema.Schema)
		checkDescriptions = func(s *jsonschema.Schema) {
			if s == nil {
				return
			}
			for name, property := range s.Properties {
				if property.Description == "" || strings.HasPrefix(property.Description, "mcp.result.") {
					t.Errorf("%s output %s has no description", tool.Name, name)
				}
				checkDescriptions(property)
			}
			checkDescriptions(s.Items)
			for _, branch := range s.OneOf {
				checkDescriptions(branch)
			}
			for _, definition := range s.Defs {
				checkDescriptions(definition)
			}
		}
		checkDescriptions(&schema)
		if strings.Contains(string(data), `"terminal_name"`) {
			t.Fatalf("%s still publishes terminal_name", tool.Name)
		}
		if strings.Contains(string(data), `"next_action"`) {
			t.Fatalf("%s still publishes next_action", tool.Name)
		}
		if tool.Annotations == nil {
			t.Fatalf("%s missing behavior annotations", tool.Name)
		}
		if tool.Name == "session_list" && (!tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint) {
			t.Fatal("session_list must be read-only and idempotent")
		}
		if tool.Name == "session_control" {
			if tool.Annotations.ReadOnlyHint || tool.Annotations.IdempotentHint || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
				t.Fatalf("%s has incorrect mutation annotations", tool.Name)
			}
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		schemas[tool.Name] = resolved
	}
	seen := map[string]bool{}
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		result, err := admin.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("%s: %v %v", name, result, err)
		}
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var structured, text map[string]any
		if err := json.Unmarshal(data, &structured); err != nil {
			t.Fatal(err)
		}
		if len(result.Content) != 1 {
			t.Fatalf("%s must return one text block", name)
		}
		content := result.Content[0].(*mcp.TextContent).Text
		if name == "create_client" {
			if content != structured["display_text"] || !strings.HasPrefix(content, "Windows one-click connection command:\n") {
				t.Fatalf("create_client must return display_text as plain text: %s", content)
			}
		} else if json.Unmarshal([]byte(content), &text) != nil || !reflect.DeepEqual(structured, text) {
			t.Fatalf("%s text and structured results differ", name)
		}
		if err := schemas[name].Validate(structured); err != nil {
			t.Fatalf("%s violates its result schema: %v", name, err)
		}
		if _, exists := structured["next_action"]; exists {
			t.Fatalf("%s still returns next_action", name)
		}
		if name == "execute_command" || name == "session_read" {
			assertCompactReadResult(t, structured)
		}
		seen[name] = true
		return structured
	}
	created := call("create_client", map[string]any{})
	token := created["client_token"].(string)
	call("list_clients", map[string]any{"client_token": token})
	waited := call("list_clients", map[string]any{"client_token": token, "wait_ms": 20})
	if waited["status"] != "waiting" || len(waited["clients"].([]any)) != 0 {
		t.Fatalf("unexpected connection timeout result: %v", waited)
	}
	ws, clientID := h.connect(t, token, "schema device")
	enableP2PTestClient(h, clientID)
	go serveTestDevice(ws, "schema", nil, nil)
	call("p2p_transfer_start", map[string]any{"client_token": token, "client_id": clientID})
	connected := call("list_clients", map[string]any{"client_token": token, "wait_ms": 300000})
	if connected["status"] != "connected" || len(connected["clients"].([]any)) != 1 {
		t.Fatalf("unexpected 300-second connection check result: %v", connected)
	}
	for _, wait := range []int{-1, 300001} {
		h.call(t, token, "list_clients", map[string]any{"wait_ms": wait}, true)
	}
	call("execute_command", map[string]any{"client_token": token, "command": "example"})
	started := call("execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "client_token": token, "command": "interactive"})
	id := started["session_id"].(string)
	call("session_read", map[string]any{"client_token": token, "session_id": id})
	call("session_control", map[string]any{"action": "input", "client_token": token, "session_id": id, "input": "yes\n"})
	call("session_list", map[string]any{"client_token": token})
	call("session_control", map[string]any{"action": "interrupt", "client_token": token, "session_id": id})
	call("session_control", map[string]any{"action": "eof", "client_token": token, "session_id": id})
	stopped := call("session_control", map[string]any{"action": "stop", "client_token": token, "session_id": id})
	// This fake device reports a normal exit, not cancellation. The server must
	// expose that distinction even though a stop request preceded completion.
	if stopped["status"] != "finished" || stopped["canceled"] != false || stopped["done"] != true {
		t.Fatal(stopped)
	}
	call("session_read", map[string]any{"client_token": token, "session_id": id})
	call("session_control", map[string]any{"action": "stop", "client_token": token, "session_id": id})
	if len(seen) != 7 {
		t.Fatalf("validated %d tools", len(seen))
	}
}
