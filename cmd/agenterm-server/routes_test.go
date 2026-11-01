package main

import (
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gorilla/websocket"
)

func TestPublicURLNormalization(t *testing.T) {
	for _, raw := range []string{
		"https://example.com", "https://example.com/",
		"https://example.com/agenterm", "https://example.com/agenterm/",
	} {
		u, err := parsePublicURL(raw, protocol.PathPrefix)
		if err != nil || u.String() != "https://example.com/agenterm" {
			t.Fatalf("normalize %q: %v %v", raw, u, err)
		}
	}
	for _, raw := range []string{
		"ws://example.com", "https://user@example.com", "https://example.com/other",
		"https://example.com/agenterm/agenterm", "https://example.com?query",
		"https://example.com?", "https://example.com#fragment", "https://example.com/%61genterm",
	} {
		if _, err := parsePublicURL(raw, protocol.PathPrefix); err == nil {
			t.Fatalf("invalid public-url accepted: %q", raw)
		}
	}
}

func TestCustomPublicURLNormalization(t *testing.T) {
	for _, tc := range []struct{ raw, prefix, want string }{
		{"https://example.com", "/a", "https://example.com/a"},
		{"https://example.com/a/", "/a/", "https://example.com/a"},
		{"https://example.com", "/tools/remote", "https://example.com/tools/remote"},
		{"https://example.com/", "/", "https://example.com"},
		{"https://example.com", "", "https://example.com"},
	} {
		u, err := parsePublicURL(tc.raw, tc.prefix)
		if err != nil || u.String() != tc.want {
			t.Fatalf("normalize %q with %q: %v %v", tc.raw, tc.prefix, u, err)
		}
	}
	for _, tc := range []struct{ raw, prefix string }{
		{"https://example.com/agenterm", "/a"}, {"https://example.com/a", "/"},
		{"https://example.com", "a"}, {"https://example.com", "/a/.."},
	} {
		if _, err := parsePublicURL(tc.raw, tc.prefix); err == nil {
			t.Errorf("invalid URL/prefix accepted: %q %q", tc.raw, tc.prefix)
		}
	}
}

func TestURLPrefixEndToEnd(t *testing.T) {
	for _, prefix := range []string{"/agenterm", "/a", "/tools/remote", ""} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			s := &server{adminKey: "test-admin"}
			ts := httptest.NewUnstartedServer(nil)
			t.Cleanup(ts.Close)
			origin := "http://" + ts.Listener.Addr().String()
			s.clientURL = "ws" + strings.TrimPrefix(origin, "http") + prefix + "/client"
			ts.Config.Handler = s.routes("v1.2.3", fstest.MapFS{"SHA256SUMS": {Data: []byte("manifest")}})
			ts.Start()

			req, _ := http.NewRequest("POST", origin+prefix+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`))
			setMCPHeaders(req)
			req.Header.Set("Authorization", "Bearer test-admin")
			res, err := ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatalf("MCP status: %d", res.StatusCode)
			}
			issued, err := s.createClient("")
			if err != nil {
				t.Fatal(err)
			}
			value := issued["invitation"].(string)
			decoded, _ := protocol.Decode(value)
			base := origin + prefix
			shell, powershell := installCommands(value)
			for i, command := range []string{shell, powershell} {
				path := "/c/" + installCode(decoded.Secret)
				if i == 1 {
					path += ".ps1"
				}
				if !strings.Contains(command, "'"+base+path+"'") {
					t.Fatalf("command lost prefix: %s", command)
				}
				res, err := ts.Client().Get(base + path)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil || res.StatusCode != 200 || !strings.Contains(string(body), value) || !strings.Contains(string(body), base+"/downloads/v1.2.3") {
					t.Fatalf("installer lost invitation or prefix: status=%d err=%v", res.StatusCode, err)
				}
			}
			res, err = ts.Client().Get(base + "/downloads/v1.2.3/SHA256SUMS")
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatalf("download status: %d", res.StatusCode)
			}
			probe, _, err := websocket.DefaultDialer.Dial(s.clientURL+"?probe=1", http.Header{version.Header: {version.Current}, "Authorization": {"Bearer " + value}})
			if err != nil {
				t.Fatal(err)
			}
			probe.Close()
			token, err := invite.Redeem(context.Background(), value)
			if err != nil || token != issued["client_token"] {
				t.Fatalf("redeem with prefix: %v", err)
			}
			credential, _ := protocol.Decode(token)
			ws, _, err := websocket.DefaultDialer.Dial(credential.URL, http.Header{version.Header: {version.Current}, "Authorization": {"Bearer " + credential.Secret}})
			if err != nil {
				t.Fatal(err)
			}
			defer ws.Close()
			deadline := time.Now().Add(time.Second)
			for len(s.listClients(credential.Secret)) != 1 {
				if time.Now().After(deadline) {
					t.Fatal("client did not connect through prefix")
				}
				time.Sleep(time.Millisecond)
			}
			if prefix != protocol.PathPrefix {
				res, err = ts.Client().Get(origin + protocol.PathPrefix + "/c/" + installCode(decoded.Secret))
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
				if res.StatusCode != 404 {
					t.Fatalf("default prefix still exposed: %d", res.StatusCode)
				}
			}
		})
	}
}

func TestOnlyNamespacedRoutesAreExposed(t *testing.T) {
	s := &server{adminKey: "test-admin"}
	h := s.routes("v1.2.3", fstest.MapFS{"SHA256SUMS": {Data: []byte("manifest")}})
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/agenterm/mcp", 401},
		{"GET", "/agenterm/client", 401},
		{"GET", "/agenterm/invite/redeem", 405},
		{"GET", "/agenterm/downloads/v1.2.3/SHA256SUMS", 200},
		{"GET", "/agenterm/c/unknown", 401},
		{"POST", "/mcp", 404},
		{"GET", "/client", 404},
		{"POST", "/invite/redeem", 404},
		{"GET", "/downloads/v1.2.3/SHA256SUMS", 404},
		{"GET", "/c/unknown", 404},
		{"GET", "/other/agenterm/mcp", 404},
		{"GET", "/agenterm/agenterm/mcp", 404},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("%s %s: got %d want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/agenterm/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`))
	setMCPHeaders(r)
	r.Header.Set("Authorization", "Bearer test-admin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"agenterm"`) {
		t.Fatalf("namespaced MCP initialize: %d %s", w.Code, w.Body.String())
	}
}
