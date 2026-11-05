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
	path := "/c/" + issued["short_code"].(string)
	paths := []string{path, path + ".ps1", "/c/" + decoded.Secret, "/c/" + decoded.Secret + ".ps1"}
	for _, scriptPath := range paths {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", scriptPath, nil)
		// Scripts must use the configured server address, never the request Host.
		r.Host = "untrusted.example"
		s.installWrapper("v1.2.3").ServeHTTP(w, r)
		body := w.Body.String()
		expected := value
		if strings.HasPrefix(scriptPath, path) {
			expected = ts.URL + "/agenterm/c/" + issued["short_code"].(string)
		}
		if w.Code != 200 || !strings.Contains(body, expected) || !strings.Contains(body, ts.URL+"/agenterm/downloads/v1.2.3") || strings.Contains(body, "untrusted.example") || strings.Contains(body, "@RELEASE_TAG@") {
			t.Fatalf("invalid wrapper %s: %d %s", scriptPath, w.Code, body)
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
	for _, scriptPath := range paths {
		w := httptest.NewRecorder()
		s.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest("GET", scriptPath, nil))
		expected := multi || strings.HasPrefix(scriptPath, path)
		marker := value
		if strings.HasPrefix(scriptPath, path) {
			marker = ts.URL + "/agenterm/c/" + issued["short_code"].(string)
		}
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), marker) != expected {
			t.Fatalf("wrapper after redemption %s: %d", scriptPath, w.Code)
		}
	}
	if len(s.shortCodes) != 1 {
		t.Fatal("short code unexpectedly consumed")
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
		{"GET", "v1.2.3", path + ".ps1", 200},
		{"GET", "v1.2.3", "/c/" + decoded.Secret, 200},
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
		want := http.StatusOK
		if i == failureLimit {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("guess %d: got %d want %d", i+1, w.Code, want)
		}
	}
	w := httptest.NewRecorder()
	s.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest("GET", "/c/"+issued["short_code"].(string), nil))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "300" {
		t.Fatalf("blocked source fetched a valid code: %d %v", w.Code, w.Header())
	}
}

func TestShortInstallLinkAfterRuntimeRestart(t *testing.T) {
	original := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/agenterm/client"}
	issued, err := original.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	value := issued["invitation"].(string)
	fresh := &runtimeState{enableShortCode: true, clientURL: original.clientURL}
	for _, suffix := range []string{"", ".ps1"} {
		w := httptest.NewRecorder()
		fresh.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/c/"+issued["short_code"].(string)+suffix, nil))
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), value) || !strings.Contains(w.Body.String(), "https://example.com/agenterm/downloads/v1.2.3") {
			t.Fatalf("empty credential wrapper after restart: %d", w.Code)
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
