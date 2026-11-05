package service

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestShortCodeStore(t *testing.T) {
	store := make(shortCodeStore)
	now := time.Unix(100, 0)
	code, err := store.issue("token", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 6 || strings.Trim(code, shortCodeAlphabet) != "" {
		t.Fatalf("invalid code %q", code)
	}
	if token, ok := store.lookup(code, now.Add(shortCodeTTL-time.Nanosecond)); !ok || token != "token" {
		t.Fatalf("lookup: %q %v", token, ok)
	}
	if _, ok := store.lookup(code, now.Add(shortCodeTTL)); ok {
		t.Fatal("expired code accepted")
	}
}

func TestShortCodeLifetimeIndependentOfInvitation(t *testing.T) {
	s := &runtimeState{clientURL: "wss://example.com/agenterm/client"}
	issued, err := s.createClientMode("", false, 1)
	if err != nil {
		t.Fatal(err)
	}
	code := issued["short_code"].(string)
	decoded, err := protocol.Decode(issued["invitation"].(string))
	if err != nil {
		t.Fatal(err)
	}
	invitation := s.invitations[decoded.Secret]
	short := s.shortCodes[code]
	if !short.expires.After(invitation.expires) {
		t.Fatal("short code expires with invitation")
	}
	s.cleanupTokensLocked(invitation.expires.Add(time.Millisecond))
	if _, ok := s.invitations[decoded.Secret]; ok {
		t.Fatal("invitation retained after expiry")
	}
	if token, ok := s.shortCodes.lookup(code, invitation.expires.Add(time.Millisecond)); !ok || token != issued["client_token"] {
		t.Fatal("short code lost when invitation expired")
	}
	s.cleanupTokensLocked(short.expires)
	if _, ok := s.shortCodes[code]; ok {
		t.Fatal("expired short code retained")
	}
	if _, ok := s.tokens[issued["client_token"].(string)]; ok {
		t.Fatal("expired unused token retained")
	}
}

func TestRevocationRemovesShortCode(t *testing.T) {
	s := &runtimeState{clientURL: "wss://example.com/agenterm/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	token := issued["client_token"].(string)
	s.revokeTokensLocked(map[string]bool{s.tokens[token]: true})
	if _, ok := s.shortCodes[issued["short_code"].(string)]; ok {
		t.Fatal("revoked token retained its short code")
	}
}

func TestShortCodeExpiryDoesNotExpireInvitation(t *testing.T) {
	s := &runtimeState{clientURL: "wss://example.com/agenterm/client"}
	issued, err := s.createClientMode("", false, 600)
	if err != nil {
		t.Fatal(err)
	}
	code := issued["short_code"].(string)
	decoded, err := protocol.Decode(issued["invitation"].(string))
	if err != nil {
		t.Fatal(err)
	}
	expires := s.shortCodes[code].expires
	s.cleanupTokensLocked(expires)
	if _, ok := s.shortCodes[code]; ok {
		t.Fatal("expired short code retained")
	}
	if _, ok := s.invitations[decoded.Secret]; !ok {
		t.Fatal("short code expiry removed invitation")
	}
	if _, ok := s.tokens[issued["client_token"].(string)]; !ok {
		t.Fatal("short code expiry removed token")
	}
}

func TestShortCodeLookupEndpoint(t *testing.T) {
	s := &runtimeState{clientURL: "wss://example.com/agenterm/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	code := issued["short_code"].(string)
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/c/"+code, nil)
		r.Header.Set(version.Header, version.Current)
		s.installWrapper("v1.2.3").ServeHTTP(w, r)
		return w
	}
	incompatible := httptest.NewRecorder()
	bad := httptest.NewRequest(http.MethodPost, "/c/"+code, nil)
	bad.Header.Set(version.Header, "1.0.0")
	s.installWrapper("v1.2.3").ServeHTTP(incompatible, bad)
	if incompatible.Code != http.StatusUpgradeRequired {
		t.Fatalf("version rejection: %d", incompatible.Code)
	}
	head := httptest.NewRecorder()
	s.installWrapper("v1.2.3").ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/c/"+code, nil))
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status: %d", head.Code)
	}
	w := request()
	var out map[string]string
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out["token"] != issued["client_token"] {
		t.Fatalf("lookup status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(); w.Code != http.StatusUnauthorized {
		t.Fatalf("used code returned %d", w.Code)
	}
}

func TestShortCodeConcurrentLookupSingleUse(t *testing.T) {
	s := &runtimeState{clientURL: "wss://example.com/agenterm/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	path := "/c/" + issued["short_code"].(string)
	var successes atomic.Int32
	var unexpected atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, path, nil)
			r.Header.Set(version.Header, version.Current)
			s.installWrapper("v1.2.3").ServeHTTP(w, r)
			if w.Code == http.StatusOK {
				successes.Add(1)
			} else if w.Code != http.StatusUnauthorized {
				unexpected.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 || unexpected.Load() != 0 {
		t.Fatalf("successes=%d unexpected=%d", successes.Load(), unexpected.Load())
	}
}

func TestMultiDeviceDoesNotSupportShortCodes(t *testing.T) {
	s := &runtimeState{clientURL: "wss://example.com/agenterm/client"}
	issued, err := s.createClientMode("", true, defaultInvitationTimeoutSeconds)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"short_code", "short_code_expires_at", "short_code_url"} {
		if _, ok := issued[field]; ok {
			t.Fatalf("multi-device output includes %s", field)
		}
	}
	if len(s.shortCodes) != 0 {
		t.Fatal("multi-device mode registered a short code")
	}
	token := issued["client_token"].(string)
	s.shortCodes = shortCodeStore{"ABC123": {token: token, expires: time.Now().Add(time.Minute)}}
	req := httptest.NewRequest(http.MethodPost, "/c/ABC123", nil)
	req.Header.Set(version.Header, version.Current)
	w := httptest.NewRecorder()
	s.installWrapper("v1.2.3").ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("multi-device short code lookup: %d", w.Code)
	}
	if strings.Contains(issued["display_text"].(string), "Short Code:") {
		t.Fatal("multi-device instructions contain a short code")
	}
}
