package service

import (
	terminal "agenterm/internal/client"
	"agenterm/internal/version"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agenterm/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestMultipleClientsAndTokenIsolation(t *testing.T) {
	s := &runtimeState{tokens: make(map[string]string), clients: make(map[string]*client)}
	mux := http.NewServeMux()
	mux.HandleFunc("/agenterm/client", s.connect)
	mux.HandleFunc("/agenterm/client/", s.connect)
	mux.HandleFunc("/agenterm/mcp", s.mcp)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	var tokens []string
	controls := make(map[string]string)
	s.clientURL = "ws" + strings.TrimPrefix(ts.URL, "http") + protocol.ClientPath
	for i := 0; i < 2; i++ {
		issued, err := s.createClient("")
		if err != nil {
			t.Fatal(err)
		}
		token := issued["client_token"].(string)
		tokens = append(tokens, token)
		controls[token] = issued["control_token"].(string)
	}
	for i, token := range tokens {
		secret := s.tokens[token]
		h := http.Header{version.Header: {version.Current}, "Authorization": []string{"Bearer " + secret}, protocol.ClientAPIHeader: {terminal.APIVersion}}
		decoded, _ := protocol.Decode(token)
		raw, _, err := websocket.DefaultDialer.Dial(decoded.ConnectionURL(), h)
		if err != nil {
			t.Fatal(err)
		}
		ws := wrapTestDevice(raw)
		defer ws.Close()
		label := fmt.Sprintf("client-%d", i)
		go serveTestDevice(ws, label, nil, nil)
	}
	call := func(token, name string, args map[string]any) (int, map[string]any) {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		req, _ := http.NewRequest("POST", ts.URL+"/agenterm/mcp", bytes.NewReader(body))
		setMCPHeaders(req)
		if token != "" {
			if control, ok := controls[token]; ok {
				token = control
			}
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result map[string]any
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode, result
	}
	if status, _ := call("", "execute_command", nil); status != http.StatusUnauthorized {
		t.Fatalf("missing token: %d", status)
	}
	if status, _ := call("invalid", "execute_command", nil); status != http.StatusUnauthorized {
		t.Fatalf("invalid token: %d", status)
	}
	for i, token := range tokens {
		status, result := call(token, "execute_command", map[string]any{"command": "test"})
		raw, _ := json.Marshal(result)
		if status != http.StatusOK || !strings.Contains(string(raw), fmt.Sprintf("client-%d", i)) {
			t.Fatalf("wrong routing: %s", raw)
		}
	}
	_, result := call(tokens[0], "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "command": "test"})
	payload := result["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	var session map[string]any
	if err := json.Unmarshal([]byte(payload), &session); err != nil {
		t.Fatal(err)
	}
	id := session["session_id"].(string)
	_, denied := call(tokens[1], "session_read", map[string]any{"session_id": id})
	if denied["result"].(map[string]any)["isError"] != true {
		t.Fatalf("cross-token session access: %#v", denied)
	}
	_, allowed := call(tokens[0], "session_read", map[string]any{"session_id": id, "wait_ms": 1000})
	if allowed["result"].(map[string]any)["isError"] == true {
		t.Fatalf("owner cannot read: %#v", allowed)
	}
	secret := s.tokens[tokens[0]]
	h := http.Header{version.Header: {version.Current}, "Authorization": []string{"Bearer " + secret}, protocol.ClientAPIHeader: {terminal.APIVersion}}
	decoded, _ := protocol.Decode(tokens[0])
	_, resp, err := websocket.DefaultDialer.Dial(decoded.ConnectionURL(), h)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("duplicate connection accepted: %v", err)
	}
	resp.Body.Close()
}
