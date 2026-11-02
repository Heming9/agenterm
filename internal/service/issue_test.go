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
		{"default", map[string]any{}, 5 * time.Minute},
		{"default multi-device", map[string]any{"multi_device": true}, 5 * time.Minute},
		{"custom", map[string]any{"timeout_seconds": 60}, time.Minute},
		{"custom multi-device", map[string]any{"timeout_seconds": 120, "multi_device": true}, 2 * time.Minute},
		{"longer than default", map[string]any{"timeout_seconds": 600}, 10 * time.Minute},
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
			if entry.expires.Before(before.Add(tc.want)) || entry.expires.After(after.Add(tc.want)) || !ownerExpiry.Equal(entry.expires) {
				t.Fatalf("invitation expiry %s, owner expiry %s, want lifetime %s", entry.expires, ownerExpiry, tc.want)
			}
			expires, err := time.Parse(time.RFC3339, created["expires_at"].(string))
			if err != nil || !expires.Equal(entry.expires.Truncate(time.Second)) {
				t.Fatalf("expires_at: %v %v", created["expires_at"], err)
			}
			next := created["next_action"].(map[string]any)
			if next["until"] != created["expires_at"] {
				t.Fatalf("wait deadline differs from invitation expiry: %v", next)
			}
			if tc.args["multi_device"] == true && !strings.Contains(created["display_text"].(string), fmt.Sprintf("%d", int64(tc.want/time.Second))) {
				t.Fatalf("multi-device notice does not show configured lifetime: %v", created["display_text"])
			}
			// Expiry must revoke both platform links and the pending credential.
			h.s.mu.Lock()
			h.s.cleanupTokensLocked(entry.expires.Add(-time.Nanosecond))
			_, retained := h.s.invitations[decoded.Secret]
			h.s.cleanupTokensLocked(entry.expires)
			remaining := len(h.s.invitations) + len(h.s.installCodes) + len(h.s.tokens) + len(h.s.clientOwners)
			h.s.mu.Unlock()
			if !retained || remaining != 0 {
				t.Fatalf("expiry cleanup: retained before=%v remaining at deadline=%d", retained, remaining)
			}
			for _, suffix := range []string{"", ".ps1"} {
				w := httptest.NewRecorder()
				h.s.installWrapper(installerTestTag).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/c/"+installCode(decoded.Secret)+suffix, nil))
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("expired installer %q: HTTP %d", suffix, w.Code)
				}
			}
			if _, err := invite.Redeem(context.Background(), created["invitation"].(string)); err == nil {
				t.Fatal("expired invitation could still be redeemed")
			}
		})
	}
}

func TestCreateClientRejectsInvalidTimeout(t *testing.T) {
	h := newMultiHarness(t, 0)
	for _, value := range []any{0, -1, 1.5, "60", maxInvitationTimeoutSeconds + 1} {
		h.call(t, "admin-test", "create_client", map[string]any{"timeout_seconds": value}, true)
	}
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	if len(h.s.tokens)+len(h.s.invitations)+len(h.s.installCodes)+len(h.s.clientOwners) != 0 {
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
		if timeout.Type != "integer" || timeout.Description == "" || timeout.Default != 300 || timeout.Minimum != 1 || timeout.Maximum != maxInvitationTimeoutSeconds {
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
	s := &runtimeState{clientURL: "wss://example.com/agenterm/client", tokens: make(map[string]string)}
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
	s := &runtimeState{clientURL: "wss://example.com/agenterm/client"}
	result, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	display := result["display_text"].(string)
	for _, part := range []string{"连接 token：\n" + result["client_token"].(string), "无客户端", "macOS / Linux 一键连接命令：", "Windows 一键连接命令："} {
		if !strings.Contains(display, part) {
			t.Fatalf("display missing %q: %s", part, display)
		}
	}
	for _, key := range []string{"macos_linux_command", "windows_powershell_command"} {
		command := result[key].(string)
		decoded, _ := protocol.Decode(result["invitation"].(string))
		code := installCode(decoded.Secret)
		if !regexp.MustCompile(`^[A-Z0-9]{6}$`).MatchString(code) {
			t.Fatalf("invalid invitation code: %q", code)
		}
		url := "https://example.com/agenterm/c/" + code
		if key == "windows_powershell_command" {
			url += ".ps1"
			if !strings.HasPrefix(command, `powershell -NoP -c "iex(irm -TimeoutSec 15 `) {
				t.Fatalf("Windows command must work in CMD and skip profiles: %s", command)
			}
		}
		if !strings.Contains(display, command) || !strings.Contains(command, "'"+url+"'") || strings.Contains(command, decoded.Secret) || strings.Contains(command, "agenterm:") || strings.Contains(command, "/downloads/") {
			t.Fatalf("invalid %s in display: %s", key, display)
		}
	}
}

func TestManagementCreatesAndUsesClient(t *testing.T) {
	s := &runtimeState{adminKey: "admin-test", tokens: map[string]string{}, clients: map[string]*client{}, invitations: map[string]invitation{}}
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
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		value, err := decodeToolText(payload.Result.Content[0].Text, payload.Result.IsError)
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
	invitation := created["invitation"].(string)
	got, err := invite.Redeem(context.Background(), invitation)
	if err != nil || got != token {
		t.Fatalf("redeem: %q %v", got, err)
	}
	if _, err := invite.Redeem(context.Background(), invitation); err == nil {
		t.Fatal("invitation redeemed twice")
	}
	status, denied := call(token, "create_client", `{}`)
	if status != http.StatusBadRequest {
		t.Fatalf("client may create: status=%d %v", status, denied)
	}
	_, missing := call("admin-test", "list_clients", `{}`)
	if missing["isError"] != true {
		t.Fatalf("missing client accepted: %v", missing)
	}
	args, _ := json.Marshal(map[string]any{"client_token": token})
	_, waited := call("admin-test", "list_clients", string(args))
	if len(waited["clients"].([]any)) != 0 {
		t.Fatalf("wait: %v", waited)
	}
	_, old := call(token, "list_clients", `{}`)
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
			s := &runtimeState{adminKey: "test-key", noPassword: tc.noPassword}
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
