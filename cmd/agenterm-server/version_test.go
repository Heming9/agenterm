package main

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const installerTestTag = "v" + version.Current

func TestConnectionVersionMismatchPreservesInvitation(t *testing.T) {
	s := &server{clientURL: "ws://example.com" + protocol.ClientPath, clients: make(map[string]*client)}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	value := issued["invitation"].(string)
	invitation, _ := protocol.Decode(value)
	token, _ := protocol.Decode(issued["client_token"].(string))
	for _, peer := range []string{"0.2.13-rc.1", "0.2.99-rc.1", "0.1.13", "0.2.28", "0.4.0", "1.2.13", "", "invalid"} {
		for _, endpoint := range []string{"redeem", "probe", "connect"} {
			r := httptest.NewRequest("GET", protocol.ClientPath, nil)
			r.Header.Set(version.Header, peer)
			r.Header.Set("Authorization", "Bearer "+token.Secret)
			w := httptest.NewRecorder()
			switch endpoint {
			case "redeem":
				r.Method = http.MethodPost
				r.Header.Set("Authorization", "Bearer "+invitation.Secret)
				s.redeemInvitation(w, r)
			case "probe":
				r.URL.RawQuery = "probe=1"
				r.Header.Set("Authorization", "Bearer "+value)
				s.connect(w, r)
			case "connect":
				s.connect(w, r)
			}
			if w.Code != http.StatusUpgradeRequired || w.Header().Get(version.Header) != version.Current || !strings.Contains(w.Body.String(), version.Current) {
				t.Fatalf("%s peer=%q: %d %s", endpoint, peer, w.Code, w.Body.String())
			}
			if len(s.clients) != 0 || len(s.invitations) != 1 {
				t.Fatal("incompatible connection changed server state")
			}
		}
	}
	// Repeated version failures must not consume the invitation or count as bad
	// credentials. A client with a different patch number can still redeem it.
	r := httptest.NewRequest("POST", "/agenterm/invite/redeem", nil)
	r.Header.Set("Authorization", "Bearer "+invitation.Secret)
	r.Header.Set(version.Header, "v0.3.999+build")
	w := httptest.NewRecorder()
	s.redeemInvitation(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), issued["client_token"].(string)) {
		t.Fatalf("patch-compatible redemption: %d %s", w.Code, w.Body.String())
	}
}
