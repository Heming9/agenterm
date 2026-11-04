package service

import (
	"agenterm/internal/protocol"
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

func TestSessionControlRejectsInvalidArgumentsBeforeSending(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, _ := h.connect(t, token, "validation")
	forwarded := make(chan protocol.Message, 32)
	go func() {
		for {
			var m protocol.Message
			if ws.ReadJSON(&m) != nil {
				return
			}
			if m.Type == "session_exec" {
				m = acceptTestSession(ws, m)
			}
			switch m.Type {
			case "input", "interrupt", "eof", "cancel":
				forwarded <- m
				if m.Type == "input" && m.RequestID != "" {
					_ = ws.WriteJSON(protocol.Message{Type: "control_done", ID: m.RequestID})
				}
			}
		}
	}()
	id := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "command": "interactive"}, false).(map[string]any)["session_id"].(string)
	for _, args := range []map[string]any{
		{},
		{"action": "unknown"},
		{"action": "input"},
		{"action": "input", "input": nil},
		{"action": "input", "input": 123},
		{"action": "input", "input": "", "wait_ms": 0},
		{"action": "interrupt", "input": ""},
		{"action": "interrupt", "wait_ms": 0},
		{"action": "eof", "input": "ignored"},
		{"action": "eof", "wait_ms": 1000},
		{"action": "stop", "input": ""},
		{"action": "stop", "wait_ms": -1},
		{"action": "stop", "wait_ms": 20001},
		{"action": "stop", "wait_ms": 1.5},
	} {
		args["session_id"] = id
		h.call(t, token, "session_control", args, true)
	}
	// This final write is a barrier: rejected calls must not have sent anything
	// to the client, even if their unrelated parameters were empty or zero.
	result := h.call(t, token, "session_control", map[string]any{"session_id": id, "action": "input", "input": "barrier"}, false).(map[string]any)
	if len(result) != 1 || result["ok"] != true {
		t.Fatalf("input invented completion fields: %v", result)
	}
	select {
	case m := <-forwarded:
		if m.Type != "input" || m.Input != "barrier" {
			t.Fatalf("invalid arguments had side effects: %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("valid input did not reach client")
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

func TestSessionControlArgumentValidationWithoutSDK(t *testing.T) {
	// Presence checks also run before forwarding, independently of SDK defaults
	// and schema validation. Stop's omitted wait defaults only inside its branch.
	for _, args := range []sessionActionArgs{
		{SessionID: "s", Action: "input", Input: new(""), WaitMS: new(0)},
		{SessionID: "s", Action: "input"},
		{SessionID: "s", Action: "eof", Input: new("")},
		{SessionID: "s", Action: "interrupt", WaitMS: new(0)},
		{SessionID: "s", Action: "stop", WaitMS: new(20001)},
		{SessionID: "s", Action: "unsupported"},
	} {
		if err := args.validate(); err == nil {
			t.Fatalf("invalid arguments accepted: %+v", args)
		} else if _, got := controlCall[any](context.Background(), localControl{&runtimeState{}}, "session/control", toolRequest[sessionActionArgs]{Args: args}); got == nil || got.Error() != err.Error() {
			t.Fatalf("runtime must reject invalid arguments before session lookup: %+v: %v", args, got)
		}
	}
}
