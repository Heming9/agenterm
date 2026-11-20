package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Simulate an API 14 runtime retained during a frontend-only deployment.
// Its display template is old, but all credentials are still authoritative.
type legacyDisplayControl struct {
	control
	issued chan map[string]any
}

func (c *legacyDisplayControl) call(ctx context.Context, method string, in, out any) error {
	if method == "config" {
		config, err := controlCall[runtimeConfig](ctx, c.control, method, in)
		config.ConnectionModePolicy = false
		*out.(*runtimeConfig) = config
		return err
	}
	if method != "create" {
		return c.control.call(ctx, method, in, out)
	}
	result, err := controlCall[map[string]any](ctx, c.control, method, in)
	if err != nil {
		return err
	}
	result["display_text"] = "Legacy runtime connection instructions"
	result["macos_linux_command"] = "legacy shell command"
	result["windows_powershell_command"] = "legacy PowerShell command"
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return err
	}
	c.issued <- result
	return nil
}

func TestFrontendFormatsInstructionsFromRetainedRuntime(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "multi"}[multi], func(t *testing.T) {
			state := &runtimeState{enableShortCode: true, adminKey: "admin-test", clientURL: "ws://example.com/agenterm/client"}
			legacy := &legacyDisplayControl{control: localControl{state}, issued: make(chan map[string]any, 1)}
			front := &frontend{control: legacy, clientURL: state.clientURL}
			server := httptest.NewServer(http.HandlerFunc(front.mcp))
			defer server.Close()
			session := connectMCPClient(t, &multiHarness{s: state, ts: server}, "admin-test")
			restricted, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_client", Arguments: map[string]any{"connection_mode": "temporary"}})
			if err != nil || !restricted.IsError {
				t.Fatalf("a retained runtime must reject temporary-only issuance: %v", err)
			}
			state.mu.Lock()
			pending := len(state.tokens)
			state.mu.Unlock()
			if pending != 0 {
				t.Fatal("temporary-only issuance must not create an unrestricted token")
			}
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_client", Arguments: map[string]any{"multi_device": multi, "timeout_seconds": 120}})
			if err != nil || result.IsError {
				t.Fatalf("frontend create failed: %v", err)
			}
			data, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var structured map[string]any
			if err := json.Unmarshal(data, &structured); err != nil {
				t.Fatal(err)
			}
			text := structured["display_text"].(string)
			issued := <-legacy.issued
			token := issued["client_token"].(string)
			code, _ := issued["short_code"].(string)
			shell, powershell := installCommands(token, code)
			if !strings.HasPrefix(text, "Windows one-click connection command:\n```\n"+powershell+"\n```") || !strings.Contains(text, "macOS / Linux one-click connection command:\n```\n"+shell+"\n```") || !strings.Contains(text, "How to connect:") || !strings.Contains(text, "Invitation validity: 120 seconds") {
				t.Fatal("retained runtime did not receive the new frontend template")
			}
			if strings.Contains(text, "Multi-device mode:") != multi {
				t.Fatal("wrong device-mode notice")
			}
			if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != text {
				t.Fatal("text and display_text differ")
			}
			if len(structured) != 3 || structured["control_token"] != issued["control_token"] || structured["expires_at"] != issued["expires_at"] {
				t.Fatal("frontend must return only its display text and the runtime control token and expiry")
			}
			if displayInvitationLink(t, text) != browserInvitationURL(token, code) {
				t.Fatal("frontend displayed the wrong browser link")
			}
		})
	}
}
