package main

import (
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestProbeDoesNotRedeemOrConnect(t *testing.T) {
	s := &server{clients: make(map[string]*client)}
	ts := httptest.NewServer(s.routes("", nil))
	defer ts.Close()
	target, _ := url.Parse(ts.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, protocol.PathPrefix+"/") {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer gateway.Close()
	s.clientURL = "ws" + strings.TrimPrefix(gateway.URL, "http") + protocol.ClientPath
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	value := issued["invitation"].(string)
	token, _ := protocol.Decode(issued["client_token"].(string))
	for i := 0; i < 2; i++ {
		ws, _, err := websocket.DefaultDialer.Dial(s.clientURL+"?probe=1", http.Header{version.Header: {version.Current}, "Authorization": {"Bearer " + value}})
		if err != nil {
			t.Fatal(err)
		}
		_ = ws.SetReadDeadline(time.Now().Add(time.Second))
		_, _, err = ws.ReadMessage()
		ws.Close()
		if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
			t.Fatalf("probe should close without sending commands: %v", err)
		}
		s.mu.Lock()
		connected := len(s.clients)
		invitations := len(s.invitations)
		s.mu.Unlock()
		if connected != 0 || invitations != 1 {
			t.Fatalf("probe changed state: clients=%d invitations=%d", connected, invitations)
		}
	}
	got, err := invite.Redeem(context.Background(), value)
	if err != nil || got != issued["client_token"] {
		t.Fatalf("redeem after probing: %q %v", got, err)
	}
	if ws, response, err := websocket.DefaultDialer.Dial(s.clientURL+"?probe=1", http.Header{version.Header: {version.Current}, "Authorization": {"Bearer " + value}}); err == nil {
		ws.Close()
		t.Fatal("redeemed invitation accepted")
	} else {
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("redeemed probe status: %d", response.StatusCode)
		}
	}
	// A genuine connection still registers after a probe.
	ws, _, err := websocket.DefaultDialer.Dial(s.clientURL, http.Header{version.Header: {version.Current}, "Authorization": {"Bearer " + token.Secret}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	deadline := time.Now().Add(time.Second)
	for len(s.listClients(token.Secret)) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("real connection did not register")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestProbeRejectsInvalidInvitations(t *testing.T) {
	s := &server{clientURL: "ws://example.com/agenterm/client"}
	issued, _ := s.createClient("")
	value := issued["invitation"].(string)
	decoded, _ := protocol.Decode(value)
	otherServer, _ := protocol.Encode(protocol.Token{URL: "ws://other.example/agenterm/client", Secret: decoded.Secret})
	for _, header := range []string{"", "Bearer invalid", "Bearer " + issued["client_token"].(string), "Bearer " + otherServer} {
		r := httptest.NewRequest(http.MethodGet, "/agenterm/client?probe=1", nil)
		r.Header.Set("Authorization", header)
		w := httptest.NewRecorder()
		s.connect(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("invalid probe status %d", w.Code)
		}
	}
	entry := s.invitations[decoded.Secret]
	entry.expires = time.Now().Add(-time.Second)
	s.invitations[decoded.Secret] = entry
	r := httptest.NewRequest(http.MethodGet, "/agenterm/client?probe=1", nil)
	r.Header.Set("Authorization", "Bearer "+value)
	w := httptest.NewRecorder()
	s.connect(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired probe status %d", w.Code)
	}
}
