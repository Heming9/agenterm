package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFailureLimitAcrossEndpoints(t *testing.T) {
	s := &server{tokens: map[string]string{"valid-token": "valid-secret"}, clients: make(map[string]*client)}
	for i := 0; i < failureLimit; i++ {
		path := "/agenterm/mcp"
		if i%2 == 1 {
			path = "/agenterm/client"
		}
		req := httptest.NewRequest("POST", path, nil)
		req.RemoteAddr = "192.0.2.1:1234"
		req.Header.Set("Authorization", "Bearer invalid")
		rec := httptest.NewRecorder()
		if path == "/agenterm/mcp" {
			s.mcp(rec, req)
		} else {
			s.connect(rec, req)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	req := httptest.NewRequest("POST", "/agenterm/mcp", strings.NewReader(`{}`))
	req.RemoteAddr = "192.0.2.1:7777"
	req.Header.Set("Authorization", "Bearer valid-token")
	rec := httptest.NewRecorder()
	s.mcp(rec, req)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("blocked source: %d", rec.Code)
	}
	req.RemoteAddr = "192.0.2.2:7777"
	rec = httptest.NewRecorder()
	s.mcp(rec, req)
	if rec.Code == http.StatusTooManyRequests || rec.Code == http.StatusUnauthorized {
		t.Fatalf("other source blocked: %d", rec.Code)
	}
}

func TestFailureLimiterExpiry(t *testing.T) {
	var l authLimiter
	now := time.Now()
	for i := 0; i < failureLimit; i++ {
		if !l.check("192.0.2.1:1", false, now) {
			t.Fatal("blocked too early")
		}
	}
	if l.check("192.0.2.1:2", true, now.Add(time.Second)) {
		t.Fatal("not blocked")
	}
	if !l.check("192.0.2.1:2", true, now.Add(failureBlock)) {
		t.Fatal("block did not expire")
	}
}
