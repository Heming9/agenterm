package main

import (
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInstallWrapperLifecycle(t *testing.T) {
	s := &server{}
	mux := http.NewServeMux()
	mux.Handle("/c/", s.installWrapper("v1.2.3"))
	mux.HandleFunc("/agenterm/invite/redeem", s.redeemInvitation)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	s.clientURL = "ws" + strings.TrimPrefix(ts.URL, "http") + protocol.ClientPath
	issued, err := s.createClient()
	if err != nil {
		t.Fatal(err)
	}
	value := issued["invitation"].(string)
	decoded, _ := protocol.Decode(value)
	path := "/c/" + decoded.Secret
	for _, suffix := range []string{"", ".ps1"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path+suffix, nil)
		// Scripts must use the configured server address, never the request Host.
		r.Host = "untrusted.example"
		s.installWrapper("v1.2.3").ServeHTTP(w, r)
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, value) || !strings.Contains(body, ts.URL+"/agenterm/downloads/v1.2.3") || strings.Contains(body, "untrusted.example") || strings.Contains(body, "@RELEASE_TAG@") {
			t.Fatalf("invalid wrapper %s: %d %s", suffix, w.Code, body)
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("credential-bearing script headers: %v", w.Header())
		}
	}
	w := httptest.NewRecorder()
	s.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest("HEAD", path, nil))
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("HEAD wrapper: %d %q", w.Code, w.Body.String())
	}
	// Fetching either platform's script (or HEAD) must not consume the invitation.
	got, err := invite.Redeem(context.Background(), value)
	if err != nil || got != issued["client_token"] {
		t.Fatalf("redeem after wrapper fetch: %q %v", got, err)
	}
	for _, suffix := range []string{"", ".ps1"} {
		w := httptest.NewRecorder()
		s.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest("GET", path+suffix, nil))
		if w.Code != 401 || strings.Contains(w.Body.String(), value) {
			t.Fatalf("redeemed wrapper still available: %d", w.Code)
		}
	}
}

func TestInstallWrapperRejectsInvalidRequests(t *testing.T) {
	s := &server{clientURL: "wss://example.com/agenterm/client"}
	issued, _ := s.createClient()
	decoded, _ := protocol.Decode(issued["invitation"].(string))
	path := "/c/" + decoded.Secret
	entry := s.invitations[decoded.Secret]
	entry.expires = time.Now().Add(-time.Second)
	s.invitations[decoded.Secret] = entry
	for _, tc := range []struct {
		method, tag, path string
		status            int
	}{
		{"GET", "v1.2.3", path, 401},
		{"GET", "v1.2.3", "/c/unknown", 401},
		{"GET", "v1.2.3", path + "/install.sh", 401},
		{"POST", "v1.2.3", path, 405},
		{"GET", "", path, 404},
	} {
		w := httptest.NewRecorder()
		s.installWrapper(tc.tag).ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("%s %s: got %d want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
}
