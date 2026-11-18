package service

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"github.com/gorilla/websocket"
)

type handshakePipe struct {
	*httptest.ResponseRecorder
	conn     net.Conn
	hijacked chan struct{}
}

func (p *handshakePipe) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	close(p.hijacked)
	return p.conn, bufio.NewReadWriter(bufio.NewReader(p.conn), bufio.NewWriter(p.conn)), nil
}

// The synchronous pipe holds the 101 write until the test reads or closes it.
func holdHandshake(t *testing.T, s *runtimeState, token protocol.Token, resume string) (net.Conn, <-chan struct{}) {
	t.Helper()
	server, peer := net.Pipe()
	done := make(chan struct{})
	t.Cleanup(func() {
		peer.Close()
		<-done
	})
	req := httptest.NewRequest(http.MethodGet, token.ConnectionURL(), nil)
	req.Header = http.Header{
		"Connection": {"Upgrade"}, "Upgrade": {"websocket"},
		"Sec-Websocket-Version": {"13"}, "Sec-Websocket-Key": {"dGhlIHNhbXBsZSBub25jZQ=="},
		"Authorization": {"Bearer " + token.Secret}, version.Header: {version.Protocol},
		protocol.ResumeCredentialHeader: {resume},
	}
	p := &handshakePipe{httptest.NewRecorder(), server, make(chan struct{})}
	go func() {
		defer close(done)
		s.connect(p, req)
	}()
	select {
	case <-p.hijacked:
	case <-done:
		t.Fatalf("upgrade rejected with status %d", p.Code)
	case <-time.After(2 * time.Second):
		t.Fatal("handshake did not start")
	}
	return peer, done
}

func TestBlockedHandshakeAllowsOtherDeviceAndRevocation(t *testing.T) {
	h := newMultiHarness(t, 1)
	a, err := protocol.Decode(h.create(t, false))
	if err != nil {
		t.Fatal(err)
	}
	other, err := h.s.createClient("192.0.2.2")
	if err != nil {
		t.Fatal(err)
	}
	b, err := protocol.Decode(other["client_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	peer, finished := holdHandshake(t, h.s, a, "")
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	headers := http.Header{"Authorization": {"Bearer " + b.Secret}, version.Header: {version.Protocol}}
	conn, _, err := dialer.Dial(b.ConnectionURL(), headers)
	if err != nil {
		t.Fatalf("independent connection failed while first handshake was held: %v", err)
	}
	defer conn.Close()
	if clients, err := h.s.waitForClients(t.Context(), b.Secret, time.Second); err != nil || len(clients) != 1 {
		t.Fatalf("independent device did not register: %v", err)
	}
	headers.Set("Authorization", "Bearer "+a.Secret)
	duplicate, resp, err := dialer.Dial(a.ConnectionURL(), headers)
	if duplicate != nil {
		duplicate.Close()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("second claim on single-device token: %v, %v", resp, err)
	}
	resp.Body.Close()
	if _, err := h.s.closeClients(t.Context(), a.Secret, nil); err != nil {
		t.Fatal(err)
	}
	peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	reader := bufio.NewReader(peer)
	upgraded, err := http.ReadResponse(reader, nil)
	if err != nil || upgraded.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("reading handshake: %v, %v", upgraded, err)
	}
	// Drain any close frame and wait for the server to close the upgraded socket.
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatal(err)
	}
	<-finished
	if got := h.s.listClients(a.Secret); len(got) != 0 {
		t.Fatalf("revoked device registered: %v", got)
	}
}

func TestFailedTakeoverKeepsCurrentDevice(t *testing.T) {
	h := newMultiHarness(t, 1)
	token, err := protocol.Decode(h.create(t, false))
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{"Authorization": {"Bearer " + token.Secret}, version.Header: {version.Protocol}}
	first, resp, err := websocket.DefaultDialer.Dial(token.ConnectionURL(), headers)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	active, err := h.s.waitForClients(t.Context(), token.Secret, time.Second)
	if err != nil || len(active) != 1 {
		t.Fatalf("initial device did not register: %v", err)
	}
	resume := resp.Header.Get(protocol.ResumeCredentialHeader)
	peer, finished := holdHandshake(t, h.s, token, resume)
	peer.Close() // Fail the response write.
	<-finished
	if got := h.s.listClients(token.Secret); len(got) != 1 || got[0].ID != active[0].ID {
		t.Fatalf("failed handshake displaced existing device: %v", got)
	}
	headers.Set(protocol.ResumeCredentialHeader, resume)
	second, _, err := websocket.DefaultDialer.Dial(token.ConnectionURL(), headers)
	if err != nil {
		t.Fatalf("retry failed after aborted takeover: %v", err)
	}
	defer second.Close()
}

func TestPendingMultiDeviceHandshakeReservesIPSlot(t *testing.T) {
	h := newMultiHarness(t, 1)
	token, err := protocol.Decode(h.create(t, true))
	if err != nil {
		t.Fatal(err)
	}
	peer, finished := holdHandshake(t, h.s, token, "")
	headers := http.Header{"Authorization": {"Bearer " + token.Secret}, version.Header: {version.Protocol}}
	duplicate, resp, err := websocket.DefaultDialer.Dial(token.ConnectionURL(), headers)
	if duplicate != nil {
		duplicate.Close()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second device at quota: %v, %v", resp, err)
	}
	resp.Body.Close()
	peer.Close()
	<-finished
	retry, _, err := websocket.DefaultDialer.Dial(token.ConnectionURL(), headers)
	if err != nil {
		t.Fatalf("quota slot was not released: %v", err)
	}
	defer retry.Close()
}

func TestIncompleteHandshakeDoesNotEvictUnusedInvitations(t *testing.T) {
	h := newMultiHarness(t, 4)
	device := h.create(t, true)
	h.connect(t, device, "first")
	token, err := protocol.Decode(device)
	if err != nil {
		t.Fatal(err)
	}
	oldest := h.create(t, false)
	second := h.create(t, false)
	third := h.create(t, false)
	peer, finished := holdHandshake(t, h.s, token, "")
	h.connect(t, device, "second")
	peer.Close()
	<-finished
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	if h.s.tokens[oldest] != "" || h.s.tokens[second] == "" || h.s.tokens[third] == "" {
		t.Fatal("only the completed connection should reclaim an invitation")
	}
}
