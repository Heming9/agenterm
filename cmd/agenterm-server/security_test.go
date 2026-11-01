package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFailureLimitAcrossEndpoints(t *testing.T) {
	s := &server{clientIPHeader: "X-Real-IP", tokens: map[string]string{"valid-token": "valid-secret"}, clients: make(map[string]*client)}
	for i := 0; i < failureLimit; i++ {
		path := "/agenterm/mcp"
		if i%2 == 1 {
			path = "/agenterm/client"
		}
		req := httptest.NewRequest("POST", path, nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-Real-IP", "192.0.2.1")
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
	req.RemoteAddr = "127.0.0.1:7777"
	req.Header.Set("X-Real-IP", "192.0.2.1")
	req.Header.Set("Authorization", "Bearer valid-token")
	rec := httptest.NewRecorder()
	s.mcp(rec, req)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("blocked source: %d", rec.Code)
	}
	req.Header.Set("X-Real-IP", "192.0.2.2")
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
		if !l.check("192.0.2.1", false, now) {
			t.Fatal("blocked too early")
		}
	}
	if l.check("192.0.2.1", true, now.Add(time.Second)) {
		t.Fatal("not blocked")
	}
	if !l.check("192.0.2.1", true, now.Add(failureBlock)) {
		t.Fatal("block did not expire")
	}
}

func TestAuthenticationRequiresConfiguredIPHeaderAcrossEndpoints(t *testing.T) {
	for _, endpoint := range []string{"mcp", "client", "probe", "redeem", "installer"} {
		for _, header := range []struct {
			name   string
			values []string
		}{
			{"missing", nil},
			{"empty", []string{""}},
			{"multiple", []string{"192.0.2.1", "192.0.2.2"}},
			{"comma-separated", []string{"192.0.2.1, 192.0.2.2"}},
			{"invalid", []string{"invalid"}},
			{"with-port", []string{"192.0.2.1:1234"}},
			{"scoped-ipv6", []string{"fe80::1%lo0"}},
		} {
			t.Run(endpoint+"/"+header.name, func(t *testing.T) {
				s := &server{noPassword: true, clientIPHeader: "X-Client-IP"}
				r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
				r.RemoteAddr = "203.0.113.5:1234"
				r.Header.Set("X-Forwarded-For", "192.0.2.1")
				r.Header.Set("X-Real-IP", "192.0.2.1")
				for _, value := range header.values {
					r.Header.Add("X-Client-IP", value)
				}
				w := httptest.NewRecorder()
				switch endpoint {
				case "mcp":
					s.mcp(w, r)
				case "client":
					s.connect(w, r)
				case "probe":
					r.URL.RawQuery = "probe=1"
					s.connect(w, r)
				case "redeem":
					s.redeemInvitation(w, r)
				case "installer":
					r.Method, r.URL.Path = http.MethodGet, "/c/AAAAAA"
					s.installWrapper("v1.2.3").ServeHTTP(w, r)
				}
				if w.Code != http.StatusBadRequest {
					t.Fatalf("expected invalid real IP rejection, got %d: %s", w.Code, w.Body.String())
				}
				if len(s.limiter.entries) != 0 {
					t.Fatal("invalid IP request polluted the authentication limiter")
				}
			})
		}
	}
}

func TestAuthenticationNormalizesRealIP(t *testing.T) {
	s := &server{noPassword: true, clientIPHeader: "X-Real-IP"}
	for i := 0; i < failureLimit; i++ {
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		r.Header.Set("X-Real-IP", "::ffff:192.0.2.1")
		r.Header.Set("Authorization", "Bearer invalid")
		w := httptest.NewRecorder()
		s.mcp(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
	r.Header.Set("X-Real-IP", "192.0.2.1")
	w := httptest.NewRecorder()
	s.mcp(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("mapped IPv4 bypassed limit: %d", w.Code)
	}
}

func TestDefaultIPSourceIgnoresHeaders(t *testing.T) {
	s := &server{noPassword: true}
	for i := 0; i < failureLimit; i++ {
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		r.RemoteAddr = "192.0.2.1:1234"
		r.Header.Set("X-Real-IP", "203.0.113.1")
		r.Header.Set("Authorization", "Bearer invalid")
		w := httptest.NewRecorder()
		s.mcp(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	r.RemoteAddr = "192.0.2.1:5678"
	r.Header.Set("X-Real-IP", "203.0.113.2")
	r.Header.Set("X-Forwarded-For", "203.0.113.3")
	w := httptest.NewRecorder()
	s.mcp(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("changing headers or peer port bypassed limit: %d", w.Code)
	}
	r.RemoteAddr = "192.0.2.2:5678"
	w = httptest.NewRecorder()
	s.mcp(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("different peer IP was blocked: %d", w.Code)
	}
}

func TestRequestIPUsesConfiguredSource(t *testing.T) {
	for _, tc := range []struct {
		name, header, remote, value, want string
		invalid                           bool
	}{
		{"peer-ipv4", "", "192.0.2.1:1234", "invalid", "192.0.2.1", false},
		{"peer-ipv6", "", "[2001:db8::1]:1234", "invalid", "2001:db8::1", false},
		{"peer-mapped-ipv4", "", "[::ffff:192.0.2.1]:1234", "invalid", "192.0.2.1", false},
		{"invalid-peer", "", "invalid", "203.0.113.1", "", true},
		{"custom-header", "CF-Connecting-IP", "192.0.2.1:1234", "203.0.113.1", "203.0.113.1", false},
		{"custom-missing", "CF-Connecting-IP", "192.0.2.1:1234", "", "", true},
		{"custom-invalid", "CF-Connecting-IP", "192.0.2.1:1234", "invalid", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &server{clientIPHeader: tc.header}
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Real-IP", "198.51.100.1")
			r.Header.Set("X-Forwarded-For", "198.51.100.2")
			if tc.value != "" {
				r.Header.Set("CF-Connecting-IP", tc.value)
			}
			got, err := s.requestIP(r)
			if got != tc.want || (err != nil) != tc.invalid {
				t.Fatalf("got %q, %v; want %q, invalid=%v", got, err, tc.want, tc.invalid)
			}
		})
	}
}
