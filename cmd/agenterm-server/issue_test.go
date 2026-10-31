package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"context"
)

func TestCreateClientWithMoreThanThousandTokens(t *testing.T) {
	s := &server{clientURL: "wss://example.com/agenterm/client", tokens: make(map[string]string)}
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
	originalTag := releaseTag
	t.Cleanup(func() { releaseTag = originalTag })
	for _, tag := range []string{"v1.2.3", ""} {
		t.Run(tag, func(t *testing.T) {
			releaseTag = tag
			s := &server{clientURL: "wss://example.com/agenterm/client"}
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
			if tag == "" {
				if !strings.Contains(display, "不可用：请使用发布版服务端") {
					t.Fatalf("missing unavailable explanation: %s", display)
				}
			} else {
				for _, key := range []string{"macos_linux_command", "windows_powershell_command"} {
					command := result[key].(string)
					decoded, _ := protocol.Decode(result["invitation"].(string))
					if !strings.Contains(display, command) || !strings.Contains(command, "/c/"+decoded.Secret) || strings.Contains(command, "agenterm:") || strings.Contains(command, "/downloads/") {
						t.Fatalf("invalid %s in display: %s", key, display)
					}
				}
			}
		})
	}
}

func TestManagementCreatesAndUsesClient(t *testing.T) {
	s := &server{adminKey: "admin-test", tokens: map[string]string{}, clients: map[string]*client{}, invitations: map[string]invitation{}}
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
		var result map[string]any
		if err := json.Unmarshal([]byte(payload.Result.Content[0].Text), &result); err != nil {
			t.Fatal(err)
		}
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
	_, denied := call(token, "create_client", `{}`)
	if denied["isError"] != true {
		t.Fatalf("client may create: %v", denied)
	}
	_, missing := call("admin-test", "wait_for_client", `{"timeout_ms":0}`)
	if missing["isError"] != true {
		t.Fatalf("missing client accepted: %v", missing)
	}
	args, _ := json.Marshal(map[string]any{"client_token": token, "timeout_ms": 0})
	_, waited := call("admin-test", "wait_for_client", string(args))
	if waited["status"] != "timeout" {
		t.Fatalf("wait: %v", waited)
	}
	_, old := call(token, "wait_for_client", `{"timeout_ms":0}`)
	if old["status"] != "timeout" {
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
			s := &server{adminKey: "test-key", noPassword: tc.noPassword}
			req := httptest.NewRequest("POST", "/agenterm/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
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
