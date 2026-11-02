package service

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestMCPCreationUsesIssuingIP(t *testing.T) {
	s := &runtimeState{noPassword: true, clientIPHeader: "X-Real-IP", maxClientsPerIP: 1, clientURL: "wss://example.com/agenterm/client"}
	call := func(remote, forwarded string) {
		t.Helper()
		r := httptest.NewRequest("POST", "/agenterm/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_client","arguments":{}}}`))
		setMCPHeaders(r)
		r.RemoteAddr = remote
		r.Header.Set("X-Real-IP", forwarded)
		w := httptest.NewRecorder()
		s.mcp(w, r)
		var payload struct {
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &payload) != nil || payload.Result.IsError {
			t.Fatalf("MCP creation failed: %d %s", w.Code, w.Body.String())
		}
	}
	call("127.0.0.1:1234", "192.0.2.1")
	call("127.0.0.1:1234", "192.0.2.2")
	call("127.0.0.1:1234", "192.0.2.1") // same issuing IP evicts its own pending token
	counts := map[string]int{}
	for _, owner := range s.clientOwners {
		counts[owner.ip]++
	}
	if len(counts) != 2 || counts["192.0.2.1"] != 1 || counts["192.0.2.2"] != 1 {
		t.Fatalf("unexpected owners: %v", counts)
	}
}

func TestRequestIP(t *testing.T) {
	s := &runtimeState{clientIPHeader: "X-Real-IP"}
	for _, tc := range []struct {
		header, want string
		invalid      bool
	}{
		{"203.0.113.2", "203.0.113.2", false},
		{"::ffff:192.0.2.1", "192.0.2.1", false},
		{"", "", true},
		{"203.0.113.2, 192.0.2.1", "", true},
		{"fe80::1%lo0", "", true},
	} {
		r := httptest.NewRequest("POST", "/mcp", nil)
		setMCPHeaders(r)
		if tc.header != "" {
			r.Header.Set("X-Real-IP", tc.header)
		}
		got, err := s.requestIP(r)
		if (err != nil) != tc.invalid || got != tc.want {
			t.Fatalf("%+v: got %q, %v", tc, got, err)
		}
	}
	r := httptest.NewRequest("POST", "/mcp", nil)
	setMCPHeaders(r)
	r.Header.Add("X-Real-IP", "192.0.2.1")
	r.Header.Add("X-Real-IP", "192.0.2.2")
	if _, err := s.requestIP(r); err == nil {
		t.Fatal("multiple X-Real-IP headers accepted")
	}
}

func TestQuotaReclaimsOldestUnusedAndExpired(t *testing.T) {
	s := &runtimeState{clientURL: "wss://example.com/agenterm/client", maxClientsPerIP: 2}
	first, _ := s.createClient("192.0.2.1")
	second, _ := s.createClient("192.0.2.1")
	other, _ := s.createClient("192.0.2.2")
	third, err := s.createClient("192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.tokens[first["client_token"].(string)]; ok {
		t.Fatal("oldest token retained")
	}
	firstInvite, _ := protocol.Decode(first["invitation"].(string))
	if _, ok := s.installCodes[installCode(firstInvite.Secret)]; ok {
		t.Fatal("reclaimed invitation retained its short code")
	}
	for _, item := range []map[string]any{second, third, other} {
		if _, ok := s.tokens[item["client_token"].(string)]; !ok {
			t.Fatal("valid token removed")
		}
	}
	decoded, _ := protocol.Decode(third["invitation"].(string))
	entry := s.invitations[decoded.Secret]
	entry.expires = time.Now().Add(-time.Second)
	s.invitations[decoded.Secret] = entry
	if _, err := s.createClient("192.0.2.2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.tokens[third["client_token"].(string)]; ok {
		t.Fatal("expired unused token retained")
	}
	if _, ok := s.installCodes[installCode(decoded.Secret)]; ok {
		t.Fatal("expired invitation retained its short code")
	}
}

func TestQuotaConcurrentConnectionsAndReconnect(t *testing.T) {
	s := &runtimeState{maxClientsPerIP: 1, tokens: map[string]string{}, clients: map[string]*client{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/agenterm/client", s.connect)
	mux.HandleFunc("/agenterm/client/", s.connect)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	s.clientURL = "ws" + strings.TrimPrefix(ts.URL, "http") + "/agenterm/client"
	issued, err := s.createClient("192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := protocol.Decode(issued["client_token"].(string))
	headers := http.Header{version.Header: {version.Current}, "Authorization": {"Bearer " + token.Secret}}
	conn, _, err := websocket.DefaultDialer.Dial(token.ConnectionURL(), headers)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.createClient("192.0.2.1"); err == nil {
		t.Fatal("creation accepted at online limit")
	}
	if _, err := s.createClient("192.0.2.2"); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		n := s.onlineForIP("192.0.2.1")
		s.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnect not detected")
		}
		time.Sleep(time.Millisecond * 10)
	}
	newInvite, err := s.createClient("192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	// An established token can reconnect; doing so reclaims the pending invite.
	conn, _, err = websocket.DefaultDialer.Dial(token.ConnectionURL(), headers)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s.mu.Lock()
	_, ok := s.tokens[newInvite["client_token"].(string)]
	s.mu.Unlock()
	if ok {
		t.Fatal("pending token not reclaimed")
	}
}

func TestQuotaConcurrentCreation(t *testing.T) {
	s := &runtimeState{clientURL: "wss://example.com/agenterm/client", maxClientsPerIP: 5}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.createClient("192.0.2.1"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(s.tokens) != 5 || len(s.invitations) != 5 {
		t.Fatalf("got %d tokens, %d invitations", len(s.tokens), len(s.invitations))
	}
}

func TestQuotaConcurrentReconnects(t *testing.T) {
	s := &runtimeState{maxClientsPerIP: 1, tokens: map[string]string{}, clients: map[string]*client{}, clientOwners: map[string]*clientOwner{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/agenterm/client", s.connect)
	mux.HandleFunc("/agenterm/client/", s.connect)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/agenterm/client"
	secrets := []string{randomBytes(protocol.TokenSecretSize), randomBytes(protocol.TokenSecretSize)}
	for _, secret := range secrets {
		token, err := protocol.Encode(protocol.Token{URL: url, Secret: secret})
		if err != nil {
			t.Fatal(err)
		}
		s.tokens[token] = secret
		s.clientOwners[secret] = &clientOwner{ip: "192.0.2.1", token: token, used: true}
	}
	type result struct {
		conn   *websocket.Conn
		status int
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, secret := range secrets {
		go func(secret string) {
			<-start
			h := http.Header{version.Header: {version.Current}, "Authorization": {"Bearer " + secret}}
			conn, resp, err := websocket.DefaultDialer.Dial(url, h)
			if err != nil {
				if resp != nil {
					results <- result{status: resp.StatusCode}
					resp.Body.Close()
				} else {
					results <- result{}
				}
				return
			}
			results <- result{conn: conn, status: http.StatusSwitchingProtocols}
		}(secret)
	}
	close(start)
	a, b := <-results, <-results
	if a.conn != nil {
		defer a.conn.Close()
	}
	if b.conn != nil {
		defer b.conn.Close()
	}
	if !((a.status == 101 && b.status == 429) || (a.status == 429 && b.status == 101)) {
		t.Fatalf("expected one connection and one limit rejection, got %d and %d", a.status, b.status)
	}
}
