package invite

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRedeemChecksServerVersion(t *testing.T) {
	compatiblePeer := version.Current[:strings.LastIndex(version.Current, ".")+1] + "999"
	for _, peer := range []string{compatiblePeer, "0.3.99", "0.2.99", "0.2.13-rc.1", "0.2.99-rc.1", version.Current + "-rc.1", "1.2.13", "", "invalid"} {
		t.Run(peer, func(t *testing.T) {
			var token string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(version.Header) != version.Current {
					t.Error("redemption did not send client version")
				}
				w.Header().Set(version.Header, peer)
				_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
			}))
			defer ts.Close()
			value, err := protocol.Encode(protocol.Token{
				URL:    "ws" + strings.TrimPrefix(ts.URL, "http") + protocol.ClientPath,
				Secret: base64.RawURLEncoding.EncodeToString(make([]byte, protocol.TokenSecretSize)),
			})
			if err != nil {
				t.Fatal(err)
			}
			token = value
			got, err := Redeem(context.Background(), value)
			if peer == compatiblePeer {
				if err != nil || got != token {
					t.Fatalf("compatible server: token=%q err=%v", got, err)
				}
			} else if err == nil || got != "" {
				t.Fatalf("incompatible server accepted: token=%q err=%v", got, err)
			}
		})
	}
}
