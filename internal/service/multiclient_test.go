package service

import (
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
	mux.HandleFunc("/agenterm/mcp", s.mcp)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	var tokens []string
	for i := 0; i < 2; i++ {
		secret := randomBytes(protocol.TokenSecretSize)
		token, err := protocol.Encode(protocol.Token{URL: "ws" + strings.TrimPrefix(ts.URL, "http") + "/agenterm/client", Secret: secret})
		if err != nil {
			t.Fatal(err)
		}
		s.tokens[token] = secret
		tokens = append(tokens, token)
	}
	for i, token := range tokens {
		secret := s.tokens[token]
		h := http.Header{version.Header: {version.Current}, "Authorization": []string{"Bearer " + secret}, protocol.PersistentSessionsHeader: {protocol.PersistentSessionsVersion}}
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/agenterm/client", h)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.Close()
		label := fmt.Sprintf("client-%d", i)
		go func() {
			for {
				var m protocol.Message
				if ws.ReadJSON(&m) != nil {
					return
				}
				if m.Type == "session_exec" {
					m = acceptTestSession(ws, m)
					if ws.WriteJSON(protocol.Message{Type: "output", ID: m.ID, Output: label}) != nil {
						return
					}
					if ws.WriteJSON(protocol.Message{Type: "command_done", ID: m.ID, RequestID: m.RequestID}) != nil {
						return
					}
					if !m.SessionPersistent {
						_ = ws.WriteJSON(protocol.Message{Type: "done", ID: m.ID})
					}
				}
			}
		}()
	}
	call := func(token, name string, args map[string]any) (int, map[string]any) {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		req, _ := http.NewRequest("POST", ts.URL+"/agenterm/mcp", bytes.NewReader(body))
		setMCPHeaders(req)
		if token != "" {
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
	_, result := call(tokens[0], "execute_command", map[string]any{"session_id": random(), "wait_ms": 0, "command": "test"})
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
	h := http.Header{version.Header: {version.Current}, "Authorization": []string{"Bearer " + secret}, protocol.PersistentSessionsHeader: {protocol.PersistentSessionsVersion}}
	_, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/agenterm/client", h)
	if err == nil || resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate connection accepted: %v", err)
	}
	resp.Body.Close()
}
