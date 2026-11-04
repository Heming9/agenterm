package service

import (
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type multiHarness struct {
	s  *runtimeState
	ts *httptest.Server
}

func newMultiHarness(t *testing.T, limit int) *multiHarness {
	t.Helper()
	s := &runtimeState{adminKey: "admin-test", maxClientsPerIP: limit}
	ts := httptest.NewServer(s.routes(installerTestTag, nil))
	t.Cleanup(ts.Close)
	s.clientURL = "ws" + strings.TrimPrefix(ts.URL, "http") + protocol.ClientPath
	return &multiHarness{s: s, ts: ts}
}

func (h *multiHarness) request(auth, name string, args map[string]any, timeout ...time.Duration) (any, bool, error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/agenterm/mcp", bytes.NewReader(body))
	setMCPHeaders(req)
	req.Header.Set("Authorization", "Bearer "+auth)
	req.Header.Set("X-Real-IP", "192.0.2.1")
	wait := 5 * time.Second
	if len(timeout) > 0 {
		wait = timeout[0]
	}
	resp, err := (&http.Client{Timeout: wait}).Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 400 {
		return nil, false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Error struct {
			Code    int
			Message string
		} `json:"error"`
		Result struct {
			StructuredContent json.RawMessage `json:"structuredContent"`
			Content           []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, false, err
	}
	if payload.Error.Code != 0 {
		return map[string]any{"error": payload.Error.Message}, true, nil
	}
	if len(payload.Result.Content) != 1 {
		return nil, false, fmt.Errorf("missing tool result")
	}
	result, err := decodeToolResult(payload.Result.Content[0].Text, payload.Result.IsError, payload.Result.StructuredContent)
	return result, payload.Result.IsError, err
}

func (h *multiHarness) call(t *testing.T, auth, name string, args map[string]any, wantError bool) any {
	t.Helper()
	result, isError, err := h.request(auth, name, args)
	if err != nil || isError != wantError {
		t.Fatalf("%s: result=%v isError=%v err=%v", name, result, isError, err)
	}
	return result
}

func (h *multiHarness) create(t *testing.T, multi bool) string {
	t.Helper()
	return h.call(t, "admin-test", "create_client", map[string]any{"multi_device": multi}, false).(map[string]any)["client_token"].(string)
}

func (h *multiHarness) dial(token, hostname string) (*websocket.Conn, *http.Response, error) {
	decoded, _ := protocol.Decode(token)
	return websocket.DefaultDialer.Dial(decoded.ConnectionURL(), http.Header{
		"Authorization": {"Bearer " + decoded.Secret}, version.Header: {version.Current},
		protocol.HostnameHeader: {base64.RawURLEncoding.EncodeToString([]byte(hostname))}, protocol.PlatformHeader: {"linux"},
		protocol.SessionControlsHeader: {"interrupt,eof"}, protocol.PersistentSessionsHeader: {protocol.PersistentSessionsVersion},
	})
}

func (h *multiHarness) connect(t *testing.T, token, hostname string) (*websocket.Conn, string) {
	t.Helper()
	ws, _, err := h.dial(token, hostname)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	deadline := time.Now().Add(time.Second)
	decoded, _ := protocol.Decode(token)
	for {
		for _, info := range h.s.listClients(decoded.Secret) {
			if info.Environment.Hostname == hostname {
				return ws, info.ID
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("client not registered")
		}
		time.Sleep(time.Millisecond)
	}
}

// Simulate creation through setup and reuse through session_id.
func acceptTestSession(ws *websocket.Conn, m protocol.Message, registries ...map[string]string) protocol.Message {
	id := m.SessionID
	created := id == ""
	explicit := !created
	if m.Setup != nil {
		id = m.Setup.SessionID
		explicit = id != ""
	}
	var detail string
	if len(registries) > 0 {
		exists := registries[0][id] != ""
		if created && explicit && exists {
			detail = "session already exists"
		}
		if !created && !exists {
			detail = "unknown session"
		}
	}
	if detail != "" {
		_ = ws.WriteJSON(protocol.Message{Type: "command_rejected", ID: m.ID, RequestID: m.RequestID, Error: detail})
		return protocol.Message{}
	}
	if id == "" {
		id = "client-" + m.ID
	}
	if len(registries) > 0 {
		registries[0][id] = id
	}
	persistent := explicit || m.WaitMS == 0
	_ = ws.WriteJSON(protocol.Message{Type: "command_accepted", ID: m.ID, RequestID: m.RequestID, SessionID: id, SessionCreate: created, SessionPersistent: persistent, SessionPromoted: created && !explicit && m.WaitMS == 0, Command: m.Command})
	m.ID, m.SessionPersistent = id, persistent
	return m
}

func serveTestDevice(ws *websocket.Conn, label string, arrived chan<- string, release <-chan struct{}) {
	names := make(map[string]string)
	for {
		var m protocol.Message
		if ws.ReadJSON(&m) != nil {
			return
		}
		switch m.Type {
		case "p2p_start":
			if registerTestP2P(m.P2P.ConfigBase64) != nil {
				return
			}
		case "session_exec":
			m = acceptTestSession(ws, m, names)
			if m.ID == "" {
				continue
			}
			if m.Command == "parallel" {
				arrived <- label
				<-release
			}
			if ws.WriteJSON(protocol.Message{Type: "output", ID: m.ID, RequestID: m.RequestID, Output: label + ":" + m.Command}) != nil {
				return
			}
			if m.Command != "interactive" {
				if ws.WriteJSON(protocol.Message{Type: "command_done", ID: m.ID, RequestID: m.RequestID}) != nil {
					return
				}
				if !m.SessionPersistent {
					_ = ws.WriteJSON(protocol.Message{Type: "done", ID: m.ID})
				}
			}
		case "input":
			if m.RequestID != "" {
				_ = ws.WriteJSON(protocol.Message{Type: "control_done", ID: m.RequestID})
			}
			if ws.WriteJSON(protocol.Message{Type: "output", ID: m.ID, Output: label + ":" + m.Input}) != nil {
				return
			}
		case "cancel":
			delete(names, m.ID)
			if ws.WriteJSON(protocol.Message{Type: "done", ID: m.ID}) != nil {
				return
			}
		case "interrupt", "eof":
			if ws.WriteJSON(protocol.Message{Type: "control_done", ID: m.RequestID}) != nil {
				return
			}
		}
	}
}

func TestMultiDeviceParallelRoutingAndIsolation(t *testing.T) {
	h := newMultiHarness(t, 0)
	token, other := h.create(t, true), h.create(t, true)
	empty := h.call(t, token, "list_client", map[string]any{}, false).(map[string]any)
	if len(empty["clients"].([]any)) != 0 {
		t.Fatal(empty)
	}
	arrived, release := make(chan string, 2), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	wsA, a := h.connect(t, token, "设备 A")
	wsB, b := h.connect(t, token, "设备 B")
	go serveTestDevice(wsA, "A", arrived, release)
	go serveTestDevice(wsB, "B", arrived, release)
	if a == b || !regexp.MustCompile(`^[a-z0-9]{6}$`).MatchString(a) || !regexp.MustCompile(`^[a-z0-9]{6}$`).MatchString(b) {
		t.Fatalf("invalid IDs: %q %q", a, b)
	}
	listed := h.call(t, "admin-test", "list_client", map[string]any{"client_token": token}, false).(map[string]any)["clients"].([]any)
	if len(listed) != 2 || listed[0].(map[string]any)["client_info"].(map[string]any)["platform"] != "linux" || listed[0].(map[string]any)["connected_at"] == "" {
		t.Fatal(listed)
	}
	if len(h.call(t, other, "list_client", map[string]any{}, false).(map[string]any)["clients"].([]any)) != 0 {
		t.Fatal("cross-token device listing")
	}
	for _, name := range []string{"execute_command"} {
		h.call(t, token, name, map[string]any{"command": "test"}, true)
		h.call(t, other, name, map[string]any{"command": "test", "client_id": a}, true)
		h.call(t, token, name, map[string]any{"command": "test", "client_id": "invalid"}, true)
	}
	results := make(chan error, 2)
	for _, target := range []struct{ id, label string }{{a, "A"}, {b, "B"}} {
		go func(id, label string) {
			out, toolError, err := h.request(token, "execute_command", map[string]any{"client_id": id, "command": "parallel"})
			if err == nil && (toolError || !strings.HasPrefix(fmt.Sprint(out.(map[string]any)["output"]), label+":parallel")) {
				err = fmt.Errorf("wrong output: %v", out)
			}
			results <- err
		}(target.id, target.label)
	}
	// Neither device completes until both have received their command. This
	// catches server-wide serialization as well as incorrect target routing.
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case label := <-arrived:
			seen[label] = true
		case <-time.After(2 * time.Second):
			t.Fatal("commands did not reach both devices concurrently")
		}
	}
	if len(seen) != 2 {
		t.Fatal(seen)
	}
	unblock()
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}

	sessions := map[string]string{}
	for _, target := range []struct{ id, label string }{{a, "A"}, {b, "B"}} {
		sid := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "client_id": target.id, "command": "interactive"}, false).(map[string]any)["session_id"].(string)
		sessions[target.label] = sid
		out := h.call(t, token, "session_read", map[string]any{"session_id": sid, "wait_ms": 1000}, false).(map[string]any)
		if out["output"] != target.label+":interactive" {
			t.Fatal(out)
		}
		h.call(t, other, "session_read", map[string]any{"session_id": sid}, true)
		h.call(t, other, "session_control", map[string]any{"action": "input", "session_id": sid, "input": "bad"}, true)
		h.call(t, other, "session_control", map[string]any{"action": "stop", "session_id": sid}, true)
		h.call(t, token, "session_control", map[string]any{"action": "input", "session_id": sid, "input": "hello"}, false)
		out = h.call(t, token, "session_read", map[string]any{"session_id": sid, "cursor": len(target.label + ":interactive"), "wait_ms": 1000}, false).(map[string]any)
		if out["output"] != target.label+":hello" {
			t.Fatal(out)
		}
	}
	wsA.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		out := h.call(t, token, "session_read", map[string]any{"session_id": sessions["A"], "wait_ms": 100}, false).(map[string]any)
		if out["status"] == "closed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnected session did not finish")
		}
	}
	h.call(t, token, "execute_command", map[string]any{"client_id": a, "command": "stale"}, true)
	out := h.call(t, token, "execute_command", map[string]any{"client_id": b, "command": "still online"}, false)
	if !strings.HasPrefix(out.(map[string]any)["output"].(string), "B:still online") {
		t.Fatal(out)
	}
	h.call(t, token, "session_control", map[string]any{"action": "stop", "session_id": sessions["B"]}, false)
	_, replacement := h.connect(t, token, "设备 A 重连")
	if replacement == a {
		t.Fatal("reconnect reused old ID")
	}
}

func TestMultiDeviceInvitationsAndQuota(t *testing.T) {
	h := newMultiHarness(t, 2)
	created := h.call(t, "admin-test", "create_client", map[string]any{"multi_device": true}, false).(map[string]any)
	value, token := created["invitation"].(string), created["client_token"].(string)
	for i := 0; i < 2; i++ {
		got, err := invite.Redeem(context.Background(), value)
		if err != nil || got != token {
			t.Fatalf("reusable redemption: %q %v", got, err)
		}
	}
	ws, _ := h.connect(t, token, "one")
	h.connect(t, token, "two")
	if conn, resp, err := h.dial(token, "three"); err == nil {
		conn.Close()
		t.Fatal("multi-device token bypassed quota")
	} else {
		if resp == nil || resp.StatusCode != 429 {
			t.Fatalf("quota: %v %v", resp, err)
		}
		resp.Body.Close()
	}
	decoded, _ := protocol.Decode(value)
	h.s.mu.Lock()
	entry := h.s.invitations[decoded.Secret]
	entry.expires = time.Now().Add(-time.Second)
	h.s.invitations[decoded.Secret] = entry
	h.s.mu.Unlock()
	if _, err := invite.Redeem(context.Background(), value); err == nil {
		t.Fatal("expired invitation redeemed")
	}
	ws.Close()
	tokenData, _ := protocol.Decode(token)
	deadline := time.Now().Add(time.Second)
	for len(h.s.listClients(tokenData.Secret)) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("disconnected device remained listed")
		}
		time.Sleep(time.Millisecond)
	}
	// Expiration cleanup must retain an established shared token and its mode.
	if _, err := h.s.createClient("192.0.2.2"); err != nil {
		t.Fatal(err)
	}
	h.connect(t, token, "reconnected after expiration")
}

func TestMCPToolDiscoveryReplacesWaitTool(t *testing.T) {
	h := newMultiHarness(t, 0)
	for _, auth := range []string{"admin-test", h.create(t, true)} {
		req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/agenterm/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		setMCPHeaders(req)
		req.Header.Set("Authorization", "Bearer "+auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		err = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, tool := range payload.Result.Tools {
			names[tool.Name] = true
		}
		want := 7
		if auth == "admin-test" {
			want = 8
		}
		if len(names) != want || !names["list_client"] || !names["close_client"] || names["list_clients"] || names["wait_for_client"] || names["wait_for_clients"] {
			t.Fatal(names)
		}
		for _, removed := range []string{"wait_for_client", "wait_for_clients"} {
			args := map[string]any{}
			if auth == "admin-test" {
				args["client_token"] = h.create(t, false)
			}
			h.call(t, auth, removed, args, true)
		}
	}
}

func TestTokenIdentifierMatchesInvitationAndRoute(t *testing.T) {
	h := newMultiHarness(t, 0)
	result := h.call(t, "admin-test", "create_client", map[string]any{}, false).(map[string]any)
	encoded := result["client_token"].(string)
	token, err := protocol.Decode(encoded)
	if err != nil || !protocol.ValidTokenIdentifier(token.Identifier) || result["token_identifier"] != token.Identifier {
		t.Fatalf("invalid identifier: %#v %v", token, err)
	}
	invitation, err := protocol.Decode(result["invitation"].(string))
	if err != nil || invitation.Identifier != token.Identifier || invitation.Secret == token.Secret {
		t.Fatalf("invitation identifier or secret: %#v %v", invitation, err)
	}
	other := "aaaaaa"
	if token.Identifier == other {
		other = "bbbbbb"
	}
	headers := http.Header{"Authorization": {"Bearer " + token.Secret}, version.Header: {version.Current}}
	ws, response, err := websocket.DefaultDialer.Dial(token.URL+"/"+other, headers)
	if ws != nil {
		ws.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong identifier accepted: %v %#v", err, response)
	}
	if response != nil {
		response.Body.Close()
	}
	ws, _, err = websocket.DefaultDialer.Dial(token.ConnectionURL(), headers)
	if err != nil {
		t.Fatal(err)
	}
	ws.Close()
}
