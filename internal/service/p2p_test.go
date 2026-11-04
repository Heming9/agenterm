package service

import (
	"agenterm/internal/p2p"
	"agenterm/internal/protocol"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func p2pTestAssets(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	data := []byte("test archive: tests that execute scripts supply a real component")
	name, _ := p2p.Filename("linux", "amd64")
	hash := sha256.Sum256(data)
	manifest := p2p.Manifest{Version: "test-1", Artifacts: []p2p.Artifact{{Platform: "linux", Arch: "amd64", File: name, SHA256: hex.EncodeToString(hash[:])}}}
	encoded, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("P2P_CLI_ASSETS_DIR", directory)
	t.Setenv("P2P_CLI_STUN", "none")
	return directory
}
func enableP2PTestClient(h *multiHarness, id string) {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	h.s.clients[id].info.Environment.P2P = true
	h.s.clients[id].info.Environment.Arch = "amd64"
}
func registerTestP2P(encoded string) error {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	var config struct {
		SignalURL string `json:"signal_url"`
		Session   string `json:"session"`
		Token     string `json:"token"`
	}
	if err = json.Unmarshal(data, &config); err != nil {
		return err
	}
	request, _ := http.NewRequest(http.MethodPost, config.SignalURL+"/v1/"+config.Session+"/register", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer "+config.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != 200 {
		return errors.New("registration failed")
	}
	return nil
}
func TestP2PStartWaitsForRegistrationAndReusesRoom(t *testing.T) {
	p2pTestAssets(t)
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, id := h.connect(t, token, "p2p")
	enableP2PTestClient(h, id)
	received := make(chan protocol.Message, 1)
	go func() {
		for {
			var message protocol.Message
			if ws.ReadJSON(&message) != nil {
				return
			}
			if message.Type == "p2p_start" {
				received <- message
				return
			}
		}
	}()
	type outcome struct {
		data   any
		failed bool
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		data, failed, err := h.request(token, "p2p_transfer_start", map[string]any{"client_id": id})
		done <- outcome{data, failed, err}
	}()
	var launch protocol.Message
	select {
	case launch = <-received:
	case <-time.After(time.Second):
		t.Fatal("Client was not activated")
	}
	if launch.Type != "p2p_start" || launch.P2P == nil {
		t.Fatal(launch.Type)
	}
	select {
	case <-done:
		t.Fatal("returned before remote registration")
	case <-time.After(20 * time.Millisecond):
	}
	if err := registerTestP2P(launch.P2P.ConfigBase64); err != nil {
		t.Fatal(err)
	}
	var first map[string]any
	select {
	case result := <-done:
		if result.err != nil || result.failed {
			t.Fatalf("%+v", result)
		}
		first = result.data.(map[string]any)
	case <-time.After(time.Second):
		t.Fatal("MCP waited for the local peer / ICE")
	}
	if first["state"] != "waiting_for_agent" || !strings.Contains(first["shell_script"].(string), "p2p-cli") || !strings.Contains(first["powershell_script"].(string), "p2p-cli") {
		t.Fatal(first)
	}
	second := h.call(t, token, "p2p_transfer_start", map[string]any{"client_id": id}, false).(map[string]any)
	if second["connection_id"] != first["connection_id"] {
		t.Fatal("retry created another connection")
	}
}
func TestP2PSignalingIsolationRolesAndImmutableSDP(t *testing.T) {
	p2pTestAssets(t)
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, id := h.connect(t, token, "signals")
	enableP2PTestClient(h, id)
	go serveTestDevice(ws, "signals", nil, nil)
	result := h.call(t, token, "p2p_transfer_start", map[string]any{}, false).(map[string]any)
	h.s.mu.Lock()
	room := h.s.p2pRooms[result["connection_id"].(string)]
	local, remote, session := room.localToken, room.remoteToken, room.id
	h.s.mu.Unlock()
	request := func(method, action, credential, body string) int {
		t.Helper()
		r, _ := http.NewRequest(method, h.ts.URL+"/agenterm/p2p/v1/"+session+"/"+action, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+credential)
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	body := `{"type":"offer","sdp":"v=0\r\na=candidate:1 1 udp 1 127.0.0.1 1 typ host\r\n"}`
	for _, check := range []struct {
		method, action, credential, body string
		status                           int
	}{
		{"GET", "offer", "wrong", "", 401}, {"GET", "offer", result["local_client_id"].(string), "", 401}, {"GET", "offer", strings.TrimPrefix(result["local_client_id"].(string), "agent-"), "", 401}, {"GET", "offer", local, "", 204}, {"PUT", "offer", remote, body, 403}, {"PUT", "offer", local, body, 200}, {"PUT", "offer", local, body, 200}, {"PUT", "offer", local, strings.ReplaceAll(body, "127.0.0.1", "127.0.0.2"), 409}, {"GET", "offer", remote, "", 200}, {"PUT", "offer", local, strings.Repeat("x", 128<<10+1), 413},
	} {
		if status := request(check.method, check.action, check.credential, check.body); status != check.status {
			t.Fatalf("%s %s=%d want %d", check.method, check.action, status, check.status)
		}
	}
	decoded, _ := protocol.Decode(token)
	other := h.create(t, false)
	otherDecoded, _ := protocol.Decode(other)
	if _, err := h.s.startP2P(context.Background(), p2pStartInput{Secret: otherDecoded.Secret, ClientID: id}); err == nil {
		t.Fatal("cross-token device access")
	}
	h.s.mu.Lock()
	h.s.closeP2PClientLocked(h.s.clients[id], "test disconnect")
	h.s.mu.Unlock()
	if status := request("GET", "offer", local, ""); status != 401 {
		t.Fatal(status)
	}
	_ = decoded
}

func TestP2PLiveRoomSurvivesSignalingCredentialExpiry(t *testing.T) {
	p2pTestAssets(t)
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, id := h.connect(t, token, "long-lived")
	enableP2PTestClient(h, id)
	go serveTestDevice(ws, "long-lived", nil, nil)
	first := h.call(t, token, "p2p_transfer_start", map[string]any{"client_id": id}, false).(map[string]any)
	h.s.mu.Lock()
	room := h.s.p2pRooms[first["connection_id"].(string)]
	room.created = time.Now().Add(-25 * time.Hour)
	credential, session := room.localToken, room.id
	h.s.mu.Unlock()
	second := h.call(t, token, "p2p_transfer_start", map[string]any{"client_id": id}, false).(map[string]any)
	if second["connection_id"] != first["connection_id"] {
		t.Fatal("signaling expiry replaced a live connection")
	}
	request, _ := http.NewRequest(http.MethodGet, h.ts.URL+"/agenterm/p2p/v1/"+session+"/offer", nil)
	request.Header.Set("Authorization", "Bearer "+credential)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired signaling credential returned %d", response.StatusCode)
	}
}

func TestP2PAgentIDUsesTokenSuffixAcrossConnections(t *testing.T) {
	p2pTestAssets(t)
	h := newMultiHarness(t, 0)
	token := h.create(t, true)
	wantAgentID := "agent-" + token[len(token)-6:]
	start := func(token string) (map[string]any, string) {
		t.Helper()
		ws, id := h.connect(t, token, "stable-agent-"+p2pRandom(4))
		enableP2PTestClient(h, id)
		go serveTestDevice(ws, "stable-agent", nil, nil)
		return h.call(t, token, "p2p_transfer_start", map[string]any{"client_id": id}, false).(map[string]any), id
	}
	first, firstClient := start(token)
	second, secondClient := start(token)
	if first["local_client_id"] != wantAgentID || second["local_client_id"] != wantAgentID {
		t.Fatalf("one token must keep one Agent ID: %v, %v", first["local_client_id"], second["local_client_id"])
	}
	if first["connection_id"] == second["connection_id"] || first["remote_client_id"] != "client-"+firstClient || second["remote_client_id"] != "client-"+secondClient {
		t.Fatal("connections and remote clients must remain distinct")
	}
	h.s.mu.Lock()
	firstRoom := h.s.p2pRooms[first["connection_id"].(string)]
	secondRoom := h.s.p2pRooms[second["connection_id"].(string)]
	firstRoom.closed = true
	firstCredential := firstRoom.localToken
	secondCredential := secondRoom.localToken
	h.s.mu.Unlock()
	if firstCredential == secondCredential {
		t.Fatal("connections must retain independent private credentials")
	}
	recreated := h.call(t, token, "p2p_transfer_start", map[string]any{"client_id": firstClient}, false).(map[string]any)
	if recreated["connection_id"] == first["connection_id"] || recreated["local_client_id"] != wantAgentID {
		t.Fatal("recreating a connection must preserve the token's Agent ID")
	}
	otherToken := h.create(t, false)
	other, _ := start(otherToken)
	if other["local_client_id"] != "agent-"+otherToken[len(otherToken)-6:] || other["local_client_id"] == wantAgentID {
		t.Fatal("different tokens must have distinct Agent IDs")
	}
}

func TestP2PStartUsesRawTokenSuffixWithoutFormatValidation(t *testing.T) {
	for _, opaque := range []string{"", "opaque-token-abc123", "tiny"} {
		t.Run("token="+opaque, func(t *testing.T) {
			p2pTestAssets(t)
			h := newMultiHarness(t, 0)
			secret := randomBytes(protocol.TokenSecretSize)
			encoded, err := protocol.Encode(protocol.Token{URL: h.s.clientURL, Secret: secret})
			if err != nil {
				t.Fatal(err)
			}
			h.s.mu.Lock()
			h.s.tokens = map[string]string{encoded: secret}
			h.s.mu.Unlock()
			ws, id := h.connect(t, encoded, "token-suffix")
			enableP2PTestClient(h, id)
			go serveTestDevice(ws, "token-suffix", nil, nil)
			token := encoded
			if opaque != "" {
				token = opaque
				h.s.mu.Lock()
				delete(h.s.tokens, encoded)
				h.s.tokens[token] = secret
				h.s.mu.Unlock()
			}
			result := h.call(t, token, "p2p_transfer_start", map[string]any{"client_id": id}, false).(map[string]any)
			want := "agent-" + token[max(0, len(token)-6):]
			if result["local_client_id"] != want || result["state"] != "waiting_for_agent" {
				t.Fatalf("token suffix should work without an Identifier field: %v", result["local_client_id"])
			}
		})
	}
}

func TestP2PBootstrapCompletionBeforeRegistrationReportsFailure(t *testing.T) {
	p2pTestAssets(t)
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, id := h.connect(t, token, "bootstrap failure")
	enableP2PTestClient(h, id)
	go func() {
		for {
			var message protocol.Message
			if ws.ReadJSON(&message) != nil {
				return
			}
			if message.Type == "p2p_start" {
				_ = ws.WriteJSON(protocol.Message{Type: "p2p_done", ID: message.ID})
				return
			}
		}
	}()
	result, failed, err := h.request(token, "p2p_transfer_start", map[string]any{}, 2*time.Second)
	if err != nil || !failed {
		t.Fatalf("bootstrap did not report termination: %v failed=%t error=%v", result, failed, err)
	}
	if !strings.Contains(result.(map[string]any)["error"].(string), "initialization failed") {
		t.Fatal(result)
	}
}
