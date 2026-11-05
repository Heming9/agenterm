package service

import (
	remoteclient "agenterm/internal/client"
	"agenterm/internal/invite"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenterm/internal/protocol"
)

func TestControlCredentialSeparatesDeviceAuthority(t *testing.T) {
	s := &runtimeState{adminKey: "admin-test", clientURL: "wss://example.com/agenterm/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	token := issued["client_token"].(string)
	control := issued["control_token"].(string)
	if !strings.HasPrefix(control, token+":") || strings.Contains(issued["display_text"].(string), control) || strings.Contains(issued["macos_linux_command"].(string), control) {
		t.Fatal("control credential missing or exposed to device")
	}
	_, err = protocol.Decode(issued["invitation"].(string))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/c/"+issued["short_code"].(string), nil))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), strings.TrimPrefix(control, token+":")) {
		t.Fatal("installer exposes control key")
	}
	call := func(auth string) int {
		r := httptest.NewRequest(http.MethodPost, "/agenterm/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_client","arguments":{}}}`))
		setMCPHeaders(r)
		r.Header.Set("Authorization", "Bearer "+auth)
		response := httptest.NewRecorder()
		s.mcp(response, r)
		return response.Code
	}
	other, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	otherToken := other["client_token"].(string)
	otherKey := strings.TrimPrefix(other["control_token"].(string), otherToken+":")
	for _, invalid := range []string{token, control + "x", token + ":", ":" + strings.TrimPrefix(control, token+":"), issued["invitation"].(string), token + ":" + otherKey} {
		if status := call(invalid); status != http.StatusUnauthorized {
			t.Fatalf("unprivileged credential accepted: %d", status)
		}
	}
	if status := call(control); status != http.StatusOK {
		t.Fatalf("control bearer rejected: %d", status)
	}
	admin := mcpPrincipal{Admin: true}
	for _, tc := range []struct {
		value string
		valid bool
	}{{token, false}, {control, true}, {control + "x", false}} {
		got, err := controlCall[string](context.Background(), localControl{s}, "target", targetRequest{Principal: admin, Token: tc.value})
		if err != nil || (got != "") != tc.valid {
			t.Fatalf("admin target validation failed: %v", err)
		}
	}
	s.mu.Lock()
	secret := s.tokens[token]
	s.revokeTokensLocked(map[string]bool{secret: true})
	s.mu.Unlock()
	if status := call(control); status != http.StatusUnauthorized {
		t.Fatalf("revoked control credential accepted: %d", status)
	}
}

// This test deliberately uses HTTP directly: multiHarness.request translates
// device tokens for legacy fixtures and must not mask the actual wire contract.
func TestShortCodeDeviceAndControlCredentialsEndToEnd(t *testing.T) {
	h := newMultiHarness(t, 0)
	issued := h.call(t, "admin-test", "create_client", map[string]any{}, false).(map[string]any)
	rawToken := issued["client_token"].(string)
	control := issued["control_token"].(string)
	invitation := issued["invitation"].(string)
	_, err := protocol.Decode(invitation)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(h.ts.URL + "/agenterm/c/" + issued["short_code"].(string))
	if err != nil {
		t.Fatal(err)
	}
	script, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(script), h.ts.URL+"/agenterm/c/"+issued["short_code"].(string)) || strings.Contains(string(script), strings.TrimPrefix(control, rawToken+":")) {
		t.Fatal("installer did not deliver only the device invitation")
	}
	redeemed, err := invite.Redeem(context.Background(), invitation)
	if err != nil || redeemed != rawToken {
		t.Fatalf("redeem: token matches=%v, err=%v", redeemed == rawToken, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- remoteclient.RunWithCallbacks(ctx, h.ts.URL+"/agenterm/c/"+issued["short_code"].(string), io.Discard, remoteclient.Callbacks{Connected: func() {
			select {
			case connected <- struct{}{}:
			default:
			}
		}})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("client did not exit")
		}
	}()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("device did not connect")
	}
	call := func(bearer, name string, args map[string]any) (int, bool) {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/agenterm/mcp", bytes.NewReader(body))
		setMCPHeaders(req)
		req.Header.Set("Authorization", "Bearer "+bearer)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var payload struct {
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if res.StatusCode == http.StatusOK && json.NewDecoder(res.Body).Decode(&payload) != nil {
			t.Fatal("invalid MCP response")
		}
		return res.StatusCode, payload.Result.IsError
	}
	if status, _ := call(rawToken, "list_client", map[string]any{}); status != http.StatusUnauthorized {
		t.Fatalf("device token authorized: %d", status)
	}
	if status, failed := call(control, "list_client", map[string]any{}); status != http.StatusOK || failed {
		t.Fatalf("control credential rejected: %d", status)
	}
	// A normal disconnect must not change the control key; reconnect with the
	// redeemed device token and exercise the same credential again.
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("first client did not disconnect")
	}
	secret, _ := protocol.Decode(rawToken)
	deadline := time.Now().Add(5 * time.Second)
	for len(h.s.listClients(secret.Secret)) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("server did not observe disconnection")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan error, 1)
	go func() {
		done <- remoteclient.RunWithCallbacks(ctx, redeemed, io.Discard, remoteclient.Callbacks{Connected: func() {
			select {
			case connected <- struct{}{}:
			default:
			}
		}})
	}()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("device did not reconnect")
	}
	if status, failed := call(control, "list_client", map[string]any{}); status != http.StatusOK || failed {
		t.Fatalf("control credential rejected after reconnect: %d", status)
	}
	if status, failed := call(control, "execute_command", map[string]any{"command": "echo control-test"}); status != http.StatusOK || failed {
		t.Fatalf("control command rejected: %d", status)
	}
	if status, failed := call(control, "close_client", map[string]any{}); status != http.StatusOK || failed {
		t.Fatalf("revoke failed: %d", status)
	}
	if status, _ := call(control, "list_client", map[string]any{}); status != http.StatusUnauthorized {
		t.Fatalf("revoked control authorized: %d", status)
	}
}
