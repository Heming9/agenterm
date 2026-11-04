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

// Simulate the API-v1 runtime retained during a frontend-only deployment.
// Its display template is old, but all credentials are still authoritative.
type legacyDisplayControl struct {
	control
	issued chan map[string]any
}

func (c *legacyDisplayControl) call(ctx context.Context, method string, in, out any) error {
	if err := c.control.call(ctx, method, in, out); err != nil {
		return err
	}
	if method == "create" {
		result := *out.(*map[string]any)
		result["display_text"] = "Legacy runtime connection instructions"
		issued := make(map[string]any, len(result))
		for key, value := range result {
			issued[key] = value
		}
		c.issued <- issued
	}
	return nil
}

func TestFrontendFormatsInstructionsFromRetainedRuntime(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "multi"}[multi], func(t *testing.T) {
			state := &runtimeState{adminKey: "admin-test", clientURL: "ws://example.com/agenterm/client"}
			legacy := &legacyDisplayControl{control: localControl{state}, issued: make(chan map[string]any, 1)}
			front := &frontend{control: legacy, clientURL: state.clientURL}
			server := httptest.NewServer(http.HandlerFunc(front.mcp))
			defer server.Close()
			session := connectMCPClient(t, &multiHarness{s: state, ts: server}, "admin-test")
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
			if !strings.HasPrefix(text, "Windows one-click connection command:\n```\n"+structured["windows_powershell_command"].(string)+"\n```") || !strings.Contains(text, "macOS / Linux one-click connection command:\n```\n"+structured["macos_linux_command"].(string)+"\n```") || !strings.Contains(text, "How to connect:") || !strings.Contains(text, "Invitation validity: 120 seconds") {
				t.Fatal("retained runtime did not receive the new frontend template")
			}
			if strings.Contains(text, "Multi-device mode:") != multi {
				t.Fatal("wrong device-mode notice")
			}
			if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != text {
				t.Fatal("text and display_text differ")
			}
			issued := <-legacy.issued
			for _, key := range []string{"client_token", "invitation", "expires_at", "macos_linux_command", "windows_powershell_command", "multi_device"} {
				if structured[key] != issued[key] {
					t.Fatalf("frontend changed runtime field %s", key)
				}
			}
		})
	}
}
