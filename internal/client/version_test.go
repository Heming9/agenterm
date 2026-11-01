package client

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestServerVersionCheckedBeforeConnectedCallback(t *testing.T) {
	for _, peer := range []string{"0.3.99", "0.2.99", "0.2.13-rc.1", "0.2.99-rc.1", "0.4.0", "1.2.13", "", "invalid"} {
		t.Run(peer, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(version.Header) != version.Current {
					t.Error("client did not send its version")
				}
				ws, err := (&websocket.Upgrader{}).Upgrade(w, r, http.Header{version.Header: {peer}})
				if err == nil {
					ws.Close()
				}
			}))
			defer ts.Close()
			token, err := protocol.Encode(protocol.Token{
				URL:    "ws" + strings.TrimPrefix(ts.URL, "http") + protocol.ClientPath,
				Secret: base64.RawURLEncoding.EncodeToString(make([]byte, protocol.TokenSecretSize)),
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			connected := false
			err = Run(ctx, token, io.Discard, func() { connected = true })
			if connected != (peer == "0.3.99") {
				t.Fatalf("peer %q: connected=%v err=%v", peer, connected, err)
			}
			if peer != "0.3.99" && err == nil {
				t.Fatal("incompatible server was accepted")
			}
		})
	}
}
