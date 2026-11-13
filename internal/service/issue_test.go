package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCreateClientInvitationTimeout(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want time.Duration
	}{
		{"default", map[string]any{}, 15 * time.Minute},
		{"default multi-device", map[string]any{"multi_device": true}, 15 * time.Minute},
		{"custom", map[string]any{"timeout_seconds": 60}, time.Minute},
		{"custom multi-device", map[string]any{"timeout_seconds": 120, "multi_device": true}, 2 * time.Minute},
		{"longer than default", map[string]any{"timeout_seconds": 1200}, 20 * time.Minute},
		{"minimum", map[string]any{"timeout_seconds": 1}, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newMultiHarness(t, 0)
			before := time.Now()
			created := h.call(t, "admin-test", "create_client", tc.args, false).(map[string]any)
			after := time.Now()
			decoded, err := protocol.Decode(created["invitation"].(string))
			if err != nil {
				t.Fatal(err)
			}
			h.s.mu.Lock()
			entry := h.s.invitations[decoded.Secret]
			ownerExpiry := h.s.clientOwners[h.s.tokens[created["client_token"].(string)]].expires
			h.s.mu.Unlock()
			if entry.expires.Before(before.Add(tc.want)) || entry.expires.After(after.Add(tc.want)) || ownerExpiry.Before(entry.expires) {
				t.Fatalf("invitation expiry %s, owner expiry %s, want lifetime %s", entry.expires, ownerExpiry, tc.want)
			}
			if isMulti(tc.args) && !ownerExpiry.Equal(entry.expires) {
				t.Fatal("multi-device token expiry was extended for a nonexistent short code")
			}
			expires, err := time.Parse(time.RFC3339, created["expires_at"].(string))
			if err != nil || !expires.Equal(entry.expires.Truncate(time.Second)) {
				t.Fatalf("expires_at: %v %v", created["expires_at"], err)
			}
			if _, exists := created["next_action"]; exists {
				t.Fatal("create_client must not return next_action")
			}
			if !strings.Contains(created["display_text"].(string), fmt.Sprintf("Invitation validity: %d seconds", int64(tc.want/time.Second))) || !strings.Contains(created["display_text"].(string), expires.UTC().Format("2006-01-02 15:04:05")+" UTC") {
				t.Fatalf("display does not show configured lifetime and deadline: %v", created["display_text"])
			}
			// Expiry revokes the pending credential while installer links remain usable.
			h.s.mu.Lock()
			h.s.cleanupTokensLocked(entry.expires.Add(-time.Nanosecond))
			_, retained := h.s.invitations[decoded.Secret]
			h.s.cleanupTokensLocked(ownerExpiry)
			remaining := len(h.s.invitations) + len(h.s.shortCodes) + len(h.s.tokens) + len(h.s.clientOwners)
			h.s.mu.Unlock()
			if !retained || remaining != 0 {
				t.Fatalf("expiry cleanup: retained before=%v remaining at deadline=%d", retained, remaining)
			}
			if code, ok := created["short_code"].(string); ok {
				for _, suffix := range []string{"", ".ps1"} {
					w := httptest.NewRecorder()
					h.s.installWrapper(installerTestTag).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/c/"+code+suffix, nil))
					if (suffix == "" && w.Code != http.StatusUnauthorized) || (suffix == ".ps1" && w.Code != http.StatusNotFound) || strings.Contains(w.Body.String(), created["invitation"].(string)) {
						t.Fatalf("expired installer %q: HTTP %d", suffix, w.Code)
					}
				}
			}
			if _, err := invite.Redeem(context.Background(), created["invitation"].(string)); err == nil {
				t.Fatal("expired invitation could still be redeemed")
			}
		})
	}
}

func isMulti(args map[string]any) bool { value, _ := args["multi_device"].(bool); return value }

func TestCreateClientRejectsInvalidTimeout(t *testing.T) {
	h := newMultiHarness(t, 0)
	for _, value := range []any{0, -1, 1.5, "60", maxInvitationTimeoutSeconds + 1} {
		h.call(t, "admin-test", "create_client", map[string]any{"timeout_seconds": value}, true)
	}
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	if len(h.s.tokens)+len(h.s.invitations)+len(h.s.shortCodes)+len(h.s.clientOwners) != 0 {
		t.Fatal("invalid timeout created credentials")
	}
}

func TestCreateClientTimeoutSchema(t *testing.T) {
	h := newMultiHarness(t, 0)
	admin := connectMCPClient(t, h, "admin-test")
	listed, err := admin.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name != "create_client" {
			continue
		}
		data, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Required   []string
			Properties map[string]struct {
				Type, Description         string
				Default, Minimum, Maximum int64
			}
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		timeout := schema.Properties["timeout_seconds"]
		if timeout.Type != "integer" || timeout.Description == "" || timeout.Default != 900 || timeout.Minimum != 1 || timeout.Maximum != maxInvitationTimeoutSeconds {
			t.Fatalf("incorrect timeout schema: %s", data)
		}
		for _, required := range schema.Required {
			if required == "timeout_seconds" {
				t.Fatal("timeout_seconds must be optional")
			}
		}
		return
	}
	t.Fatal("create_client tool missing")
}

func TestCreateClientWithMoreThanThousandTokens(t *testing.T) {
	s := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/agenterm/client", tokens: make(map[string]string)}
	for i := 0; i < 1000; i++ {
		s.tokens[fmt.Sprintf("existing-%d", i)] = "secret"
	}
	if _, err := s.createClient(""); err != nil {
		t.Fatalf("creating a client with 1000 existing tokens failed: %v", err)
	}
	if got := len(s.tokens); got != 1001 {
		t.Fatalf("got %d tokens, want 1001", got)
	}
}

func TestCreateClientDisplayText(t *testing.T) {
	t.Setenv("AGENTERM_LANG", "zh-CN")
	s := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/agenterm/client"}
	result, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	display := result["display_text"].(string)
	for _, part := range []string{"Connection token / short code ( " + result["short_code"].(string) + " ):\n```\n" + result["client_token"].(string) + "\n```", "How to connect:\n1.", "\n2.", "\n3.", "If Agenterm is already installed,", "Invitation validity: 900 seconds"} {
		if !strings.Contains(display, part) {
			t.Fatalf("display missing %q: %s", part, display)
		}
	}
	if !strings.HasSuffix(display, "Invitation validity: 900 seconds (expires at "+strings.Replace(result["expires_at"].(string), "T", " ", 1)[:19]+" UTC).") {
		t.Fatalf("unexpected validity footer: %s", display)
	}
	sections := strings.Split(display, "\n\n")
	if len(sections) != 7 || sections[3] != "Browser invitation link:\nAgenterm invites you to a remote assistance session (open the invitation link in a computer browser): "+result["short_code_url"].(string) || sections[0] != "Windows one-click connection command:\n```\n"+result["windows_powershell_command"].(string)+"\n```" || sections[1] != "macOS / Linux one-click connection command:\n```\n"+result["macos_linux_command"].(string)+"\n```" || sections[2] != "Connection token / short code ( "+result["short_code"].(string)+" ):\n```\n"+result["client_token"].(string)+"\n```" {
		t.Fatalf("incorrect connection instructions order or line breaks: %s", display)
	}
	if strings.ContainsAny(display, "*\r\x1b") || strings.Contains(display, "%!") || strings.Contains(display, "Multi-device mode:") {
		t.Fatalf("unexpected formatting or multi-device notice: %s", display)
	}
	for _, key := range []string{"macos_linux_command", "windows_powershell_command"} {
		command := result[key].(string)
		decoded, _ := protocol.Decode(result["invitation"].(string))
		code := result["short_code"].(string)
		if !regexp.MustCompile(`^[a-z0-9]{6}$`).MatchString(code) {
			t.Fatalf("invalid invitation code: %q", code)
		}
		clientToken, _ := protocol.Decode(result["client_token"].(string))
		path := "/agenterm/c/" + clientToken.Secret
		if key == "windows_powershell_command" {
			path += ".ps1"
		}
		if !strings.Contains(display, command) || !strings.Contains(command, "example.com") || !strings.Contains(command, path) || !strings.Contains(command, "https://") || strings.Contains(command, decoded.Secret) || strings.Contains(command, "Agenterm_") || strings.Contains(command, "/downloads/") {
			t.Fatalf("invalid %s in display: %s", key, command)
		}
	}
}

func TestInstallCommandsForPublicHost(t *testing.T) {
	s := &runtimeState{clientURL: "wss://agenterm.net/client"}
	result, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := protocol.Decode(result["client_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	base := "https://agenterm.net/c/" + decoded.Secret
	shell, windows := installCommands(result["client_token"].(string))
	if want := "curl -fL " + base + "|sh"; shell != want {
		t.Fatalf("shell command = %q, want %q", shell, want)
	}
	if want := `powershell -NoP "irm ` + base + `.ps1|iex"`; windows != want {
		t.Fatalf("PowerShell command = %q, want %q", windows, want)
	}
}

func TestCreateClientDisplayLocalesAndModes(t *testing.T) {
	for _, lang := range []string{"zh-CN", "en"} {
		for _, multi := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/multi=%t", lang, multi), func(t *testing.T) {
				t.Setenv("AGENTERM_LANG", lang)
				h := newMultiHarness(t, 0)
				admin := connectMCPClient(t, h, "admin-test")
				result, err := admin.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_client", Arguments: map[string]any{"multi_device": multi, "timeout_seconds": 120}})
				if err != nil || result.IsError {
					t.Fatalf("create_client: %v %v", result, err)
				}
				data, err := json.Marshal(result.StructuredContent)
				if err != nil {
					t.Fatal(err)
				}
				var structured invitationResult
				if err := json.Unmarshal(data, &structured); err != nil {
					t.Fatal(err)
				}
				var fields map[string]any
				if err := json.Unmarshal(data, &fields); err != nil {
					t.Fatal(err)
				}
				_, hasURL := fields["short_code_url"]
				_, hasCode := fields["short_code"]
				_, hasExpiry := fields["short_code_expires_at"]
				if multi {
					if hasCode || hasExpiry || hasURL || structured.ShortCode != "" || strings.Contains(structured.DisplayText, "short code ( ") || strings.Contains(structured.DisplayText, "or short code above") {
						t.Fatal("multi-device output includes a short code")
					}
				} else {
					if !hasCode || !hasExpiry || !hasURL || structured.ShortCodeURL != shortCodeURL(structured.ClientToken, structured.ShortCode) || structured.ShortCode == "" {
						t.Fatal("single-device output lacks short code or expiry")
					}
					expected := "Connection token / short code ( " + structured.ShortCode + " ):\n```\n" + structured.ClientToken + "\n```\n\nBrowser invitation link:\nAgenterm invites you to a remote assistance session (open the invitation link in a computer browser): " + structured.ShortCodeURL
					if !strings.Contains(structured.DisplayText, expected) {
						t.Fatal("short code is not directly after the token")
					}
					if !strings.Contains(structured.DisplayText, "open the browser invitation link above or run the command for your system.") || !strings.Contains(structured.DisplayText, "enter the connection token or short code above.") {
						t.Fatal("short-code connection instructions are missing")
					}
					if strings.Contains(structured.DisplayText, "Client connection address (single use") {
						t.Fatal("obsolete short-code URL section remains in display text")
					}
				}
				if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != structured.DisplayText {
					t.Fatal("MCP text must contain exactly the connection instructions")
				}
				display := structured.DisplayText
				if !strings.HasPrefix(display, "Windows one-click connection command:\n```\n"+structured.WindowsPowerShellCommand+"\n```\n\n") || !strings.Contains(display, "How to connect:\n1.") || !strings.Contains(display, "Invitation validity: 120 seconds") || strings.ContainsAny(display, "*\r\x1b") || strings.Contains(display, "%!") {
					t.Fatalf("invalid English connection instructions: %s", display)
				}
				sections := strings.Split(display, "\n\n")
				if (!multi && len(sections) != 7) || (multi && (len(sections) != 7 || !strings.HasPrefix(sections[6], "Multi-device mode:"))) || strings.Contains(display, "Multi-device mode:") != multi {
					t.Fatalf("incorrect multi-device notice: %s", display)
				}
			})
		}
	}
}

func TestManagementCreatesAndUsesClient(t *testing.T) {
	s := &runtimeState{enableShortCode: true, adminKey: "admin-test", tokens: map[string]string{}, clients: map[string]*client{}, invitations: map[string]invitation{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/agenterm/mcp", s.mcp)
	mux.HandleFunc("/agenterm/invite/redeem", s.redeemInvitation)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	s.clientURL = "ws" + ts.URL[4:] + "/agenterm/client"
	call := func(auth, name, args string) (int, map[string]any) {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
		req, _ := http.NewRequest("POST", ts.URL+"/agenterm/mcp", bytes.NewBufferString(body))
		setMCPHeaders(req)
		req.Header.Set("Authorization", "Bearer "+auth)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return res.StatusCode, nil
		}
		var payload struct {
			Result struct {
				StructuredContent json.RawMessage `json:"structuredContent"`
				Content           []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		value, err := decodeToolResult(payload.Result.Content[0].Text, payload.Result.IsError, payload.Result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		result := value.(map[string]any)
		result["isError"] = payload.Result.IsError
		return res.StatusCode, result
	}
	if status, _ := call("wrong", "create_client", `{}`); status != 401 {
		t.Fatalf("unauthorized: %d", status)
	}
	_, created := call("admin-test", "create_client", `{}`)
	if created["isError"] != false {
		t.Fatalf("create: %v", created)
	}
	token := created["client_token"].(string)
	control := created["control_token"].(string)
	invitation := created["invitation"].(string)
	got, err := invite.Redeem(context.Background(), invitation)
	if err != nil || got != token {
		t.Fatalf("redeem: %q %v", got, err)
	}
	if _, err := invite.Redeem(context.Background(), invitation); err == nil {
		t.Fatal("invitation redeemed twice")
	}
	status, denied := call(control, "create_client", `{}`)
	if status != http.StatusBadRequest {
		t.Fatalf("client may create: status=%d %v", status, denied)
	}
	_, missing := call("admin-test", "list_client", `{}`)
	if missing["isError"] != true {
		t.Fatalf("missing client accepted: %v", missing)
	}
	args, _ := json.Marshal(map[string]any{"client_token": control})
	_, waited := call("admin-test", "list_client", string(args))
	if len(waited["clients"].([]any)) != 0 {
		t.Fatalf("wait: %v", waited)
	}
	_, old := call(control, "list_client", `{}`)
	if len(old["clients"].([]any)) != 0 {
		t.Fatalf("client token compatibility: %v", old)
	}
}

func TestManagementAuthenticationModes(t *testing.T) {
	for i := 0; i < 100; i++ {
		key := randomManagementKey()
		if len(key) != 10 {
			t.Fatalf("key length: %q", key)
		}
		for _, c := range key {
			if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') {
				t.Fatalf("invalid key: %q", key)
			}
		}
	}
	for _, tc := range []struct {
		name       string
		noPassword bool
		header     string
		want       int
	}{
		{"password required", false, "", 401},
		{"anonymous management", true, "", 200},
		{"invalid credential", true, "Bearer invalid", 401},
		{"explicit management key", false, "Bearer test-key", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &runtimeState{enableShortCode: true, adminKey: "test-key", noPassword: tc.noPassword}
			req := httptest.NewRequest("POST", "/agenterm/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			setMCPHeaders(req)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rr := httptest.NewRecorder()
			s.mcp(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			if tc.want == 200 && !bytes.Contains(rr.Body.Bytes(), []byte(`"create_client"`)) {
				t.Fatalf("management tool missing: %s", rr.Body.String())
			}
		})
	}
}

func TestCreateClientMCPWithShortCodesDisabled(t *testing.T) {
	h := newMultiHarness(t, 0)
	h.s.enableShortCode = false
	admin := connectMCPClient(t, h, "admin-test")
	result, err := admin.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_client", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("create_client: %v %v", result, err)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"short_code", "short_code_expires_at"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("unexpected %s", key)
		}
	}
	if fields["control_token"] == "" || fields["client_token"] == "" || strings.Contains(fields["display_text"].(string), "short code ( ") {
		t.Fatalf("invalid result: %v", fields)
	}
}
