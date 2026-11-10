package service

import (
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInstallWrapperLifecycle(t *testing.T) {
	testInstallWrapperLifecycle(t, false)
}

func testInstallWrapperLifecycle(t *testing.T, multi bool) {
	s := &runtimeState{enableShortCode: true}
	mux := http.NewServeMux()
	mux.Handle("/c/", s.installWrapper("v1.2.3"))
	mux.HandleFunc("/agenterm/invite/redeem", s.redeemInvitation)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	s.clientURL = "ws" + strings.TrimPrefix(ts.URL, "http") + protocol.ClientPath
	issued, err := s.createClientMode("", multi, defaultInvitationTimeoutSeconds)
	if err != nil {
		t.Fatal(err)
	}
	value := issued["invitation"].(string)
	decoded, _ := protocol.Decode(value)
	device, err := protocol.Decode(issued["client_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"/c/" + device.Secret, "/c/" + device.Secret + ".ps1"}
	for _, scriptPath := range paths {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, scriptPath, nil)
		r.Host = "untrusted.example"
		s.installWrapper("v1.2.3").ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), issued["client_token"].(string)) || !strings.Contains(w.Body.String(), ts.URL+"/agenterm/downloads/v1.2.3") || strings.Contains(w.Body.String(), "untrusted.example") {
			t.Fatalf("invalid token wrapper %s: %d", scriptPath, w.Code)
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("headers: %v", w.Header())
		}
	}
	for _, suffix := range []string{"", ".ps1"} {
		w := httptest.NewRecorder()
		s.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/c/"+decoded.Secret+suffix, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("invitation secret must not serve installer: %d", w.Code)
		}
	}
	got, err := invite.Redeem(context.Background(), value)
	if err != nil || got != issued["client_token"] {
		t.Fatalf("redeem after wrapper: %q %v", got, err)
	}
	for _, scriptPath := range paths {
		w := httptest.NewRecorder()
		s.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest(http.MethodGet, scriptPath, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), issued["client_token"].(string)) {
			t.Fatalf("token script after invitation redeem: %d", w.Code)
		}
	}

}

func TestInstallWrapperRejectsInvalidRequests(t *testing.T) {
	s := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/agenterm/client"}
	issued, _ := s.createClient("")
	decoded, _ := protocol.Decode(issued["invitation"].(string))
	path := "/c/" + issued["short_code"].(string)
	entry := s.invitations[decoded.Secret]
	entry.expires = time.Now().Add(-time.Second)
	s.invitations[decoded.Secret] = entry
	for _, tc := range []struct {
		method, tag, path string
		status            int
	}{
		{"GET", "v1.2.3", path, 200},
		{"GET", "v1.2.3", path + ".ps1", 404},
		{"GET", "v1.2.3", "/c/" + decoded.Secret, 401},
		{"GET", "v1.2.3", "/c/unknown", 404},
		{"GET", "v1.2.3", path + "/install.sh", 404},
		{"PUT", "v1.2.3", path, 405},
		{"GET", "", path, 404},
	} {
		w := httptest.NewRecorder()
		s.installWrapper(tc.tag).ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("%s %s: got %d want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
}

func TestInstallWrapperLimitsShortCodeGuesses(t *testing.T) {
	s := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/agenterm/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	unknown := "AAAAAA"
	if unknown == issued["short_code"].(string) {
		unknown = "BBBBBB"
	}
	for i := 0; i <= failureLimit; i++ {
		w := httptest.NewRecorder()
		s.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest("GET", "/c/"+unknown, nil))
		want := http.StatusUnauthorized
		if i == failureLimit {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("guess %d: got %d want %d", i+1, w.Code, want)
		}
	}
	w := httptest.NewRecorder()
	s.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest("GET", "/c/"+issued["short_code"].(string), nil))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "2" {
		t.Fatalf("blocked source fetched a valid code: %d %v", w.Code, w.Header())
	}
}

func TestShortLinkAfterRuntimeRestart(t *testing.T) {
	original := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/agenterm/client"}
	issued, err := original.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	fresh := &runtimeState{enableShortCode: true, clientURL: original.clientURL}
	for _, tc := range []struct {
		suffix string
		want   int
	}{{"", http.StatusUnauthorized}, {".ps1", http.StatusNotFound}} {
		w := httptest.NewRecorder()
		fresh.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/c/"+issued["short_code"].(string)+tc.suffix, nil))
		if w.Code != tc.want {
			t.Fatalf("%s status %d", tc.suffix, w.Code)
		}
	}
}

func TestInstallerUsesTokenSecretIndependentlyOfShortCode(t *testing.T) {
	s := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/agenterm/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	token := issued["client_token"].(string)
	decoded, err := protocol.Decode(token)
	if err != nil {
		t.Fatal(err)
	}
	code := issued["short_code"].(string)
	lookup := httptest.NewRequest(http.MethodPost, "/c/"+code, nil)
	lookup.Header.Set(version.Header, version.Current)
	response := httptest.NewRecorder()
	s.installWrapper("v1.2.3").ServeHTTP(response, lookup)
	if response.Code != http.StatusOK {
		t.Fatalf("short-code lookup: %d", response.Code)
	}
	for _, suffix := range []string{"", ".ps1"} {
		for i := 0; i < 2; i++ {
			w := httptest.NewRecorder()
			s.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/c/"+decoded.Secret+suffix, nil))
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), token) || strings.Contains(w.Body.String(), issued["control_token"].(string)) {
				t.Fatalf("token installer %s: %d", suffix, w.Code)
			}
		}
	}
	s.mu.Lock()
	s.revokeTokensLocked(map[string]bool{decoded.Secret: true})
	s.mu.Unlock()
	w := httptest.NewRecorder()
	s.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/c/"+decoded.Secret, nil))
	if strings.Contains(w.Body.String(), token) {
		t.Fatal("revoked token returned by installer")
	}
}
