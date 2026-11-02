package main

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
		if len(result.Content) != 1 || json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &text) != nil || !reflect.DeepEqual(structured, text) {
			t.Fatalf("%s text and structured results differ", name)
		}
		if err := schemas[name].Validate(structured); err != nil {
			t.Fatalf("%s violates its result schema: %v", name, err)
		}
		seen[name] = true
		return structured
	}
	created := call("create_client", map[string]any{})
	token := created["client_token"].(string)
	call("list_clients", map[string]any{"client_token": token})
	ws, _ := h.connect(t, token, "schema device")
	go serveTestDevice(ws, "schema", nil, nil)
	call("list_clients", map[string]any{"client_token": token, "wait_ms": 20})
	call("execute_command", map[string]any{"client_token": token, "command": "example"})
	started := call("session_start", map[string]any{"client_token": token, "command": "interactive"})
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
