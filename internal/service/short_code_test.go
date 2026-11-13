package service

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestShortCodeStore(t *testing.T) {
	store := make(shortCodeStore)
	code, err := store.issue("token")
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 6 || strings.Trim(code, shortCodeAlphabet) != "" {
		t.Fatalf("invalid code %q", code)
	}
	for _, value := range []string{code, strings.ToUpper(code)} {
		if token, ok := store.lookup(value); !ok || token != "token" {
			t.Fatalf("lookup %q: %q %v", value, token, ok)
		}
	}
	if !store.available("client") {
		t.Fatal("client is a valid short code under the /c/ route")
	}
	store["ABC123"] = shortCodeEntry{token: "legacy"}
	if store.available("abc123") {
		t.Fatal("legacy short code collision")
	}
}

func TestShortCodeLifetimeMatchesToken(t *testing.T) {
	s := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/agenterm/client"}
	for _, seconds := range []int64{1, 300, 3600} {
		issued, err := s.createClientMode("", false, seconds)
		if err != nil {
			t.Fatal(err)
		}
		code := issued["short_code"].(string)
		decoded, err := protocol.Decode(issued["invitation"].(string))
		if err != nil {
			t.Fatal(err)
		}
		expiry := s.invitations[decoded.Secret].expires
		if !s.clientOwners[s.tokens[issued["client_token"].(string)]].expires.Equal(expiry) {
			t.Fatal("expiry mismatch")
		}
		s.cleanupTokensLocked(expiry)
		if _, ok := s.shortCodes[code]; ok {
			t.Fatal("expired short code retained")
		}
	}
}

func TestRevocationRemovesShortCode(t *testing.T) {
	s := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/agenterm/client"}
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

func TestShortCodeLookupEndpoint(t *testing.T) {
	s := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/agenterm/client"}
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
	for i := 1; i < maxShortCodeRedemptions; i++ {
		if w := request(); w.Code != http.StatusOK {
			t.Fatalf("redemption %d returned %d", i+1, w.Code)
		}
	}
	if w := request(); w.Code != http.StatusUnauthorized {
		t.Fatalf("exhausted code returned %d", w.Code)
	}
}

func TestMultiDeviceDoesNotSupportShortCodes(t *testing.T) {
	s := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/agenterm/client"}
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
	s.shortCodes = shortCodeStore{"ABC123": {token: token}}
	req := httptest.NewRequest(http.MethodPost, "/c/ABC123", nil)
	req.Header.Set(version.Header, version.Current)
	w := httptest.NewRecorder()
	s.installWrapper("v1.2.3").ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("multi-device short code lookup: %d", w.Code)
	}
	if strings.Contains(issued["display_text"].(string), "short code ( ") {
		t.Fatal("multi-device instructions contain a short code")
	}
}

func TestShortCodesDisabled(t *testing.T) {
	s := &runtimeState{clientURL: "wss://example.com/agenterm/client"}
	issued, err := s.createClientMode("", false, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"short_code", "short_code_expires_at"} {
		if _, ok := issued[key]; ok {
			t.Fatalf("disabled short code returned %s", key)
		}
	}
	if len(s.shortCodes) != 0 || strings.Contains(issued["display_text"].(string), "short code ( ") {
		t.Fatal("disabled short code issued or displayed")
	}
	if remaining := time.Until(s.clientOwners[s.tokens[issued["client_token"].(string)]].expires); remaining > time.Second {
		t.Fatalf("token lifetime unexpectedly extended: %v", remaining)
	}
	s.shortCodes = shortCodeStore{"ABC123": {token: issued["client_token"].(string)}}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/c/ABC123", nil)
		r.Header.Set(version.Header, version.Current)
		s.installWrapper("v1.2.3").ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: got %d", method, w.Code)
		}
	}
	decoded, err := protocol.Decode(issued["client_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.installWrapper("v1.2.3").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/c/"+decoded.Secret, nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), issued["client_token"].(string)) {
		t.Fatal("device token installer unavailable")
	}
}

func TestNewShortCodeCanBeRedeemedInUppercase(t *testing.T) {
	s := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	code := issued["short_code"].(string)
	redeem := func(value string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/c/"+value, nil)
		r.Header.Set(version.Header, version.Current)
		s.installWrapper("v1.2.3").ServeHTTP(w, r)
		return w.Code
	}
	if got := redeem(strings.ToUpper(code)); got != http.StatusOK {
		t.Fatalf("uppercase redemption: %d", got)
	}
	for i := 1; i < maxShortCodeRedemptions; i++ {
		if got := redeem(code); got != http.StatusOK {
			t.Fatalf("redemption %d: %d", i+1, got)
		}
	}
	if got := redeem(code); got != http.StatusUnauthorized {
		t.Fatalf("exhausted code: %d", got)
	}
}
