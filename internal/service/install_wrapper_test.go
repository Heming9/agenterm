package service

import (
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInstallWrapperLifecycle(t *testing.T) {
	for _, mode := range []struct {
		name  string
		multi bool
	}{{"single", false}, {"multi", true}} {
		t.Run(mode.name, func(t *testing.T) { testInstallWrapperLifecycle(t, mode.multi) })
	}
}

func testInstallWrapperLifecycle(t *testing.T, multi bool) {
	s := &runtimeState{}
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
	path := "/c/" + installCode(decoded.Secret)
	paths := []string{path, path + ".ps1", "/c/" + decoded.Secret, "/c/" + decoded.Secret + ".ps1"}
	for _, scriptPath := range paths {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", scriptPath, nil)
		// Scripts must use the configured server address, never the request Host.
		r.Host = "untrusted.example"
		s.installWrapper("v1.2.3").ServeHTTP(w, r)
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, value) || !strings.Contains(body, ts.URL+"/agenterm/downloads/v1.2.3") || strings.Contains(body, "untrusted.example") || strings.Contains(body, "@RELEASE_TAG@") {
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
	wantStatus, wantCodes := 401, 0
	if multi {
		wantStatus, wantCodes = 200, 1
	}
	for _, scriptPath := range paths {
		w := httptest.NewRecorder()
		s.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest("GET", scriptPath, nil))
		if w.Code != wantStatus || (!multi && strings.Contains(w.Body.String(), value)) {
			t.Fatalf("wrapper after redemption %s: %d", scriptPath, w.Code)
		}
	}
	if len(s.installCodes) != wantCodes {
		t.Fatalf("codes after redemption: got %d want %d", len(s.installCodes), wantCodes)
	}
}

func TestInstallWrapperRejectsInvalidRequests(t *testing.T) {
	s := &runtimeState{clientURL: "wss://example.com/agenterm/client"}
	issued, _ := s.createClient("")
	decoded, _ := protocol.Decode(issued["invitation"].(string))
	path := "/c/" + installCode(decoded.Secret)
	entry := s.invitations[decoded.Secret]
	entry.expires = time.Now().Add(-time.Second)
	s.invitations[decoded.Secret] = entry
	for _, tc := range []struct {
		method, tag, path string
		status            int
	}{
		{"GET", "v1.2.3", path, 401},
		{"GET", "v1.2.3", path + ".ps1", 401},
		{"GET", "v1.2.3", "/c/" + decoded.Secret, 401},
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

func TestInstallWrapperLimitsShortCodeGuesses(t *testing.T) {
	s := &runtimeState{clientURL: "wss://example.com/agenterm/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := protocol.Decode(issued["invitation"].(string))
	unknown := "AAAAAA"
	if unknown == installCode(decoded.Secret) {
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
	s.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest("GET", "/c/"+installCode(decoded.Secret), nil))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "300" {
		t.Fatalf("blocked source fetched a valid code: %d %v", w.Code, w.Header())
	}
}

func TestInstallWrapperRetriesCodeCollisions(t *testing.T) {
	first := make([]byte, protocol.TokenSecretSize)
	first[len(first)-1] = 1
	collision := make([]byte, protocol.TokenSecretSize)
	binary.BigEndian.PutUint64(collision[len(collision)-8:], 2176782337) // 36^6 + 1 also maps to 000001.
	retry := make([]byte, protocol.TokenSecretSize)
	retry[len(retry)-1] = 2
	originalReader := rand.Reader
	rand.Reader = bytes.NewReader(append(append(first, collision...), retry...))
	t.Cleanup(func() { rand.Reader = originalReader })

	s := &runtimeState{clientURL: "wss://example.com/agenterm/client"}
	expires := time.Now().Add(time.Minute)
	s.mu.Lock()
	defer s.mu.Unlock()
	firstValue, err := s.newInvitation("first-token", expires)
	if err != nil {
		t.Fatal(err)
	}
	secondValue, err := s.newInvitation("second-token", expires)
	if err != nil {
		t.Fatal(err)
	}
	firstInvite, _ := protocol.Decode(firstValue)
	secondInvite, _ := protocol.Decode(secondValue)
	if installCode(firstInvite.Secret) != "000001" || installCode(secondInvite.Secret) != "000002" {
		t.Fatal("collision retry did not preserve the original code and issue a new one")
	}
	for _, item := range []struct{ secret, token string }{{firstInvite.Secret, "first-token"}, {secondInvite.Secret, "second-token"}} {
		if secret := s.installCodes[installCode(item.secret)]; secret != item.secret || s.invitations[secret].token != item.token {
			t.Fatalf("collision replaced an invitation: %s", item.token)
		}
	}
}
