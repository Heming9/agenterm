package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestPublicURLNormalization(t *testing.T) {
	for _, raw := range []string{
		"https://example.com", "https://example.com/",
		"https://example.com/agenterm", "https://example.com/agenterm/",
	} {
		u, err := parsePublicURL(raw)
		if err != nil || u.String() != "https://example.com/agenterm" {
			t.Fatalf("normalize %q: %v %v", raw, u, err)
		}
	}
	for _, raw := range []string{
		"ws://example.com", "https://user@example.com", "https://example.com/other",
		"https://example.com/agenterm/agenterm", "https://example.com?query",
		"https://example.com?", "https://example.com#fragment", "https://example.com/%61genterm",
	} {
		if _, err := parsePublicURL(raw); err == nil {
			t.Fatalf("invalid public-url accepted: %q", raw)
		}
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
	r.Header.Set("Authorization", "Bearer test-admin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"agenterm"`) {
		t.Fatalf("namespaced MCP initialize: %d %s", w.Code, w.Body.String())
	}
}
