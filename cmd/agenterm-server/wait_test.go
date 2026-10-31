package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWaitForClientMCP(t *testing.T) {
	s := &server{tokens: map[string]string{"token-a": "secret-a", "token-b": "secret-b"}, clients: make(map[string]*client)}
	mux := http.NewServeMux()
	mux.HandleFunc("/agenterm/mcp", s.mcp)
	mux.HandleFunc("/agenterm/client", s.connect)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	call := func(token string, timeout int) string {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "wait_for_client", "arguments": map[string]any{"timeout_ms": timeout}}})
		req, _ := http.NewRequest("POST", ts.URL+"/agenterm/mcp", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "request error: " + err.Error()
		}
		defer resp.Body.Close()
		var result struct {
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return err.Error()
		}
		if result.Result.IsError || len(result.Result.Content) != 1 {
			return "tool error"
		}
		return result.Result.Content[0].Text
	}
	if got := call("token-a", 0); got != `{"status":"timeout"}` {
		t.Fatalf("immediate check: %s", got)
	}
	result := make(chan string, 1)
	go func() { result <- call("token-a", 2000) }()
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		registered := len(s.waiters["secret-a"]) == 1
		s.mu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiter not registered")
		}
		time.Sleep(time.Millisecond)
	}
	header := http.Header{"Authorization": []string{"Bearer secret-b"}}
	other, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/agenterm/client", header)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	select {
	case got := <-result:
		t.Fatalf("other client woke waiter: %s", got)
	default:
	}
	header.Set("Authorization", "Bearer secret-a")
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/agenterm/client", header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case got := <-result:
		if got != `{"status":"connected"}` {
			t.Fatalf("wait: %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not finish")
	}
	if got := call("token-a", 0); got != `{"status":"connected"}` {
		t.Fatalf("already connected: %s", got)
	}
}

func TestWaitForClientCancellationAndTimeout(t *testing.T) {
	s := &server{clients: make(map[string]*client)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.waitForClient(ctx, "secret", time.Second); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
	if got, err := s.waitForClient(context.Background(), "secret", time.Millisecond); err != nil || got != "timeout" {
		t.Fatalf("timeout: %q %v", got, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.waiters) != 0 {
		t.Fatal("waiters leaked")
	}
}
