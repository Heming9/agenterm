package service

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const installerTestTag = "v" + version.Current

func TestConnectionVersionMismatchPreservesShortCode(t *testing.T) {
	s := &runtimeState{enableShortCode: true, clientURL: "ws://example.com" + protocol.ClientPath, clients: make(map[string]*client)}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	code := issued["short_code"].(string)
	path := "/c/" + code
	installer := s.installWrapper(installerTestTag)
	token, _ := protocol.Decode(issued["client_token"].(string))
	for _, peer := range []string{"0.2.13-rc.1", "0.2.99-rc.1", "0.1.13", "0.2.28", "0.3.99", version.Protocol + "-rc.1", "1.2.13", "", "invalid"} {
		for _, endpoint := range []string{"redeem", "connect"} {
			r := httptest.NewRequest("GET", protocol.ClientPath, nil)
			r.Header.Set(version.Header, peer)
			r.Header.Set("Authorization", "Bearer "+token.Secret)
			w := httptest.NewRecorder()
			switch endpoint {
			case "redeem":
				r.Method = http.MethodPost
				r.URL.Path = path
				installer.ServeHTTP(w, r)
			case "connect":
				r.URL.Path = protocol.ClientPath + "/" + token.Identifier
				s.connect(w, r)
			}
			if w.Code != http.StatusUpgradeRequired || w.Header().Get(version.Header) != version.Protocol || !strings.Contains(w.Body.String(), version.Protocol) {
				t.Fatalf("%s peer=%q: %d %s", endpoint, peer, w.Code, w.Body.String())
			}
			if len(s.clients) != 0 || s.shortCodes[code].redemptions != 0 {
				t.Fatal("incompatible connection changed server state")
			}
		}
	}
	r := httptest.NewRequest("POST", path, nil)
	compatiblePeer := "v" + version.Protocol[:strings.LastIndex(version.Protocol, ".")+1] + "999+build"
	r.Header.Set(version.Header, compatiblePeer)
	r.Header.Set(version.ReleaseHeader, "1.2.3")
	w := httptest.NewRecorder()
	installer.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get(version.ReleaseHeader) != version.Current ||
		!strings.Contains(w.Body.String(), issued["client_token"].(string)) {
		t.Fatalf("patch-compatible redemption: %d %s", w.Code, w.Body.String())
	}
}
