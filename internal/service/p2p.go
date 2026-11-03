package service

import (
	"agenterm/internal/components"
	"agenterm/internal/p2p"
	"agenterm/internal/protocol"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type p2pStartArgs struct {
	targetArgs
	ClientID string `json:"client_id,omitempty"`
}
type p2pStartInput struct {
	Secret   string
	ClientID string
	Manifest p2p.Manifest
}
type p2pStartData struct {
	BaseURL     string
	Session     string
	LocalID     string
	RemoteID    string
	LocalConfig json.RawMessage
}
type p2pStartResult struct {
	ConnectionID     string `json:"connection_id"`
	LocalClientID    string `json:"local_client_id"`
	RemoteClientID   string `json:"remote_client_id"`
	State            string `json:"state"`
	ShellScript      string `json:"shell_script"`
	PowershellScript string `json:"powershell_script"`
}
type p2pRoom struct {
	id, owner, localToken, remoteToken, localID, remoteID, commandID string
	client                                                           *client
	remoteRegistered, closed, readyClosed                            bool
	error                                                            string
	created                                                          time.Time
	ready                                                            chan struct{}
	offer, answer                                                    []byte
}
type p2pSignalInput struct {
	Identity                       requestIdentity
	Session, Token, Action, Method string
	Body                           json.RawMessage
}
type p2pSignalResult struct {
	Status int
	Body   json.RawMessage
}

func p2pRandom(size int) string {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data)
}
func (room *p2pRoom) notify() {
	if !room.readyClosed {
		close(room.ready)
		room.readyClosed = true
	}
}
func (s *runtimeState) closeP2PClientLocked(c *client, reason string) {
	for _, room := range s.p2pRooms {
		if room.client == c && !room.closed {
			room.closed = true
			room.error = reason
			room.notify()
		}
	}
}
func (s *runtimeState) p2pCommandDoneLocked(c *client, m protocol.Message) {
	for _, room := range s.p2pRooms {
		if room.client == c && room.commandID == m.ID && !room.closed {
			room.closed = true
			if !room.remoteRegistered {
				room.error = "remote p2p-cli initialization failed"
			}
			room.notify()
		}
	}
}
func publicBase(clientURL string) string {
	base := strings.TrimSuffix(clientURL, "/client")
	base = strings.Replace(base, "wss://", "https://", 1)
	return strings.Replace(base, "ws://", "http://", 1)
}
func p2pConfig(base string, room *p2pRoom, remote bool) json.RawMessage {
	token := room.localToken
	if remote {
		token = room.remoteToken
	}
	data, _ := json.Marshal(map[string]any{"signal_url": base + "/p2p", "session": room.id, "token": token, "register": true, "timeout_seconds": 300})
	return data
}

func (s *runtimeState) startP2P(ctx context.Context, in p2pStartInput) (p2pStartData, error) {
	s.mu.Lock()
	c, err := s.targetClientLocked(in.Secret, in.ClientID)
	if err != nil {
		s.mu.Unlock()
		return p2pStartData{}, err
	}
	if !c.info.Environment.P2P {
		s.mu.Unlock()
		return p2pStartData{}, errors.New("client upgrade required for p2p-cli initialization")
	}
	if !in.Manifest.Supports(c.info.Environment.Platform, c.info.Environment.Arch) {
		s.mu.Unlock()
		return p2pStartData{}, errors.New("p2p-cli artifact unavailable for remote platform")
	}
	if s.p2pRooms == nil {
		s.p2pRooms = make(map[string]*p2pRoom)
	}
	var room *p2pRoom
	for id, item := range s.p2pRooms {
		// Signaling credentials expire separately. A live foreground command
		// still owns its connection and must remain reusable until it ends.
		if item.closed || (!item.remoteRegistered && time.Since(item.created) > 5*time.Minute) {
			delete(s.p2pRooms, id)
			continue
		}
		if item.client == c && item.owner == in.Secret {
			room = item
		}
	}
	var message *protocol.Message
	if room == nil {
		if len(s.p2pRooms) >= 1024 {
			s.mu.Unlock()
			return p2pStartData{}, errors.New("P2P connection limit reached")
		}
		var agentID string
		for encoded, secret := range s.tokens {
			if secret != in.Secret {
				continue
			}
			agentID = "agent-" + encoded[max(0, len(encoded)-6):]
			break
		}
		room = &p2pRoom{id: p2pRandom(16), owner: in.Secret, localToken: p2pRandom(32), remoteToken: p2pRandom(32), localID: agentID, remoteID: "client-" + c.info.ID, commandID: random(), client: c, created: time.Now(), ready: make(chan struct{})}
		s.p2pRooms[room.id] = room
		base := publicBase(s.clientURL)
		remoteConfig := p2pConfig(base, room, true)
		sh, ps := p2p.Scripts(in.Manifest, base, nil, true, false)
		stopSh, stopPS := p2p.Scripts(in.Manifest, base, remoteConfig, false, true)
		script, stopScript := sh, stopSh
		if c.info.Environment.Platform == "windows" {
			script, stopScript = ps, stopPS
		}
		message = &protocol.Message{Type: "p2p_start", ID: room.commandID, P2P: &protocol.P2PLaunch{Script: script, StopScript: stopScript, ConfigBase64: base64.StdEncoding.EncodeToString(p2pConfig(base, room, true))}}
	}
	s.mu.Unlock()
	if message != nil {
		if err = c.send(*message); err != nil {
			s.mu.Lock()
			room.closed = true
			room.error = "cannot start remote P2P component"
			room.notify()
			s.mu.Unlock()
			return p2pStartData{}, err
		}
	}
	// Wait only for registration. Waiting for ICE here would deadlock the Agent bootstrap.
	timer := time.NewTimer(25 * time.Second)
	defer timer.Stop()
	select {
	case <-room.ready:
	case <-ctx.Done():
		return p2pStartData{}, ctx.Err()
	case <-timer.C:
		return p2pStartData{}, errors.New("remote P2P component still initializing; retry p2p_transfer_start")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if room.closed || room.error != "" {
		return p2pStartData{}, fmt.Errorf("P2P initialization failed: %s", room.error)
	}
	return p2pStartData{BaseURL: publicBase(s.clientURL), Session: room.id, LocalID: room.localID, RemoteID: room.remoteID, LocalConfig: p2pConfig(publicBase(s.clientURL), room, false)}, nil
}

func (s *runtimeState) p2pSignal(_ context.Context, in p2pSignalInput) (p2pSignalResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	room := s.p2pRooms[in.Session]
	remote := false
	valid := false
	if room != nil && !room.closed && s.clients[room.client.info.ID] == room.client && time.Since(room.created) < 24*time.Hour {
		remote = subtle.ConstantTimeCompare([]byte(in.Token), []byte(room.remoteToken)) == 1
		local := subtle.ConstantTimeCompare([]byte(in.Token), []byte(room.localToken)) == 1
		valid = remote || local
		if !room.remoteRegistered && time.Since(room.created) > 5*time.Minute {
			valid = false
		}
	}
	if status, err := s.authorize(in.Identity.request(), valid); err != nil {
		return p2pSignalResult{}, &controlError{Status: status, Message: err.Error()}
	}
	if in.Action == "register" && in.Method == http.MethodPost {
		id := room.localID
		if remote {
			id = room.remoteID
			room.remoteRegistered = true
			room.notify()
		}
		stun := os.Getenv("P2P_CLI_STUN")
		if stun == "" {
			stun = "stun.l.google.com:19302"
		}
		body, _ := json.Marshal(map[string]any{"client_id": id, "offerer": !remote, "stun": stun})
		return p2pSignalResult{Status: 200, Body: body}, nil
	}
	if in.Action != "offer" && in.Action != "answer" {
		return p2pSignalResult{}, &controlError{Status: 404, Message: "unknown P2P signaling action"}
	}
	field := &room.offer
	if in.Action == "answer" {
		field = &room.answer
	}
	switch in.Method {
	case http.MethodGet:
		if *field == nil {
			return p2pSignalResult{Status: 204}, nil
		}
		return p2pSignalResult{Status: 200, Body: *field}, nil
	case http.MethodPut:
		if (in.Action == "offer" && remote) || (in.Action == "answer" && !remote) {
			return p2pSignalResult{}, &controlError{Status: 403, Message: "wrong P2P negotiation role"}
		}
		var signal struct {
			Type string `json:"type"`
			SDP  string `json:"sdp"`
		}
		decoder := json.NewDecoder(bytes.NewReader(in.Body))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&signal) != nil || signal.Type != in.Action || !strings.HasPrefix(signal.SDP, "v=0") || !strings.Contains(signal.SDP, "a=candidate:") {
			return p2pSignalResult{}, errors.New("invalid P2P SDP")
		}
		encoded, _ := json.Marshal(signal)
		if *field != nil && !bytes.Equal(*field, encoded) {
			return p2pSignalResult{}, &controlError{Status: 409, Message: "P2P SDP already published"}
		}
		*field = encoded
		return p2pSignalResult{Status: 200}, nil
	default:
		return p2pSignalResult{}, &controlError{Status: 405, Message: "method not allowed"}
	}
}

func (s *frontend) p2pHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/p2p/")
	if strings.HasPrefix(path, "downloads/") {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(405)
			return
		}
		manifest, err := s.p2pManifest()
		if err != nil {
			http.Error(w, "P2P assets unavailable", http.StatusServiceUnavailable)
			return
		}
		file := strings.TrimPrefix(path, "downloads/")
		for _, asset := range manifest.Artifacts {
			base := strings.TrimSuffix(asset.File, ".exe")
			requested := strings.TrimSuffix(file, ".sha256")
			compatible := base + ".tar.gz"
			if asset.Platform == "windows" {
				compatible = base + ".zip"
			}
			if requested == asset.File || requested == base+".tar.xz" || requested == compatible {
				s.p2pDownloadsOnce.Do(func() { s.p2pDownloads = components.Downloads(os.DirFS(s.p2pAssets), "p2p-cli") })
				w.Header().Del("Content-Type")
				http.ServeFileFS(w, r, s.p2pDownloads, file)
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) != 3 || parts[0] != "v1" || len(parts[1]) != 32 {
		http.NotFound(w, r)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if r.Header.Get("Authorization") != "Bearer "+token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil {
		http.Error(w, "signaling body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if len(body) == 0 {
		body = nil
	}
	out, err := controlCall[p2pSignalResult](r.Context(), s.control, "p2p/signal", p2pSignalInput{Identity: requestIdentity{Header: r.Header, RemoteAddr: r.RemoteAddr}, Session: parts[1], Token: token, Action: parts[2], Method: r.Method, Body: body})
	if err != nil {
		writeControlError(w, err)
		return
	}
	w.WriteHeader(out.Status)
	if len(out.Body) > 0 {
		_, _ = w.Write(out.Body)
	}
}

func (s *frontend) p2pManifest() (p2p.Manifest, error) {
	if s.p2pAssets == "" {
		return p2p.Manifest{}, errors.New("server components have not been initialized")
	}
	return p2p.Load(s.p2pAssets)
}

func (s *frontend) p2pTransferStart(ctx context.Context, secret string, args p2pStartArgs) (p2pStartResult, error) {
	manifest, err := s.p2pManifest()
	if err != nil {
		return p2pStartResult{}, err
	}
	data, err := controlCall[p2pStartData](ctx, s.control, "p2p/start", p2pStartInput{Secret: secret, ClientID: args.ClientID, Manifest: manifest})
	if err != nil {
		return p2pStartResult{}, err
	}
	sh, ps := p2p.Scripts(manifest, data.BaseURL, data.LocalConfig, false, false)
	return p2pStartResult{ConnectionID: data.Session, LocalClientID: data.LocalID, RemoteClientID: data.RemoteID, State: "waiting_for_agent", ShellScript: sh, PowershellScript: ps}, nil
}
