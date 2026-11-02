package service

import (
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInvitationSingleUseConcurrent(t *testing.T) {
	s := &runtimeState{invitations: map[string]invitation{"secret": {token: "connection-token", expires: time.Now().Add(time.Minute)}}}
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodPost, "/agenterm/invite/redeem", nil)
			r.Header.Set("Authorization", "Bearer secret")
			r.Header.Set(version.Header, version.Current)
			w := httptest.NewRecorder()
			s.redeemInvitation(w, r)
			statuses <- w.Code
		}()
	}
	wg.Wait()
	close(statuses)
	success, denied := 0, 0
	for code := range statuses {
		if code == http.StatusOK {
			success++
		}
		if code == http.StatusUnauthorized {
			denied++
		}
	}
	if success != 1 || denied != 1 {
		t.Fatalf("got success=%d denied=%d", success, denied)
	}
}
func TestInvitationExpired(t *testing.T) {
	s := &runtimeState{invitations: map[string]invitation{"secret": {token: "connection-token", expires: time.Now().Add(-time.Second)}}}
	r := httptest.NewRequest(http.MethodPost, "/agenterm/invite/redeem", nil)
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set(version.Header, version.Current)
	w := httptest.NewRecorder()
	s.redeemInvitation(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", w.Code)
	}
}

func TestInvitationRedeemEndToEnd(t *testing.T) {
	s := &runtimeState{invitations: make(map[string]invitation)}
	ts := httptest.NewServer(http.HandlerFunc(s.redeemInvitation))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/agenterm/client"
	secret := randomBytes(protocol.TokenSecretSize)
	value, err := protocol.Encode(protocol.Token{URL: wsURL, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	token, err := protocol.Encode(protocol.Token{URL: wsURL, Secret: randomBytes(protocol.TokenSecretSize)})
	if err != nil {
		t.Fatal(err)
	}
	s.invitations[secret] = invitation{token: token, expires: time.Now().Add(time.Minute)}
	got, err := invite.Redeem(context.Background(), value)
	if err != nil || got != token {
		t.Fatalf("redeem: token matches=%v err=%v", got == token, err)
	}
	if _, err := invite.Redeem(context.Background(), value); err == nil {
		t.Fatal("second redemption succeeded")
	}
}
