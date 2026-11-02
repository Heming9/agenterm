package service

import (
	"agenterm/internal/i18n"
	"agenterm/internal/logging"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"github.com/gorilla/websocket"
)

type client struct {
	log       *slog.Logger
	info      clientInfo
	secret    string
	conn      *websocket.Conn
	write     sync.Mutex
	mu        sync.Mutex
	pending   map[string]chan protocol.Message
	closed    bool
	latencyMS int64 // Latest heartbeat RTT; guarded by mu. Zero means unmeasured.
}

func (c *client) logger() *slog.Logger {
	if c.log != nil {
		return c.log
	}
	return slog.Default().With("client_id", c.info.ID)
}

func (c *client) send(m protocol.Message) error {
	c.write.Lock()
	defer c.write.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(protocol.WriteTimeout)); err != nil {
		return err
	}
	return c.conn.WriteJSON(m)
}

type runtimeState struct {
	startupSettings *runtimeSettings
	adminKey        string
	noPassword      bool
	clientURL       string
	clientIPHeader  string                      // empty uses the directly connected peer IP
	tokens          map[string]string           // full token -> client credential
	clients         map[string]*client          // six-character client ID -> connection
	clientWaiters   map[string]*clientWaitState // client credential -> active connection waits
	multiDevice     map[string]bool             // client credential -> multi-device mode
	mu              sync.Mutex
	sessions        map[string]*session
	executions      map[string]*execution
	limiter         authLimiter
	invitations     map[string]invitation
	installCodes    map[string]string // six-character installer code -> invitation secret
	maxClientsPerIP int
	clientOwners    map[string]*clientOwner // client secret -> issuing IP and lifecycle
	nextClientOrder uint64
}

type invitation struct {
	token   string
	expires time.Time
}

func (s *runtimeState) redeemInvitation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, i18n.Text(i18n.Terminal(), "method.not.allowed"), http.StatusMethodNotAllowed)
		return
	}
	secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	entry, ok := s.invitations[secret]
	ok = ok && r.Header.Get("Authorization") == "Bearer "+secret && time.Now().Before(entry.expires)
	if status, err := s.authorize(r, ok); err != nil {
		s.mu.Unlock()
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "300")
		}
		http.Error(w, err.Error(), status)
		return
	}
	if !acceptClientVersion(w, r) {
		s.mu.Unlock()
		return
	}
	if !s.multiDevice[s.tokens[entry.token]] {
		s.deleteInvitation(secret)
	}
	s.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": entry.token})
}

func random() string {
	return randomBytes(24)
}

func randomBytes(size int) string {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		log.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func randomManagementKey() string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	var key strings.Builder
	for i := 0; i < 10; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			log.Fatal(err)
		}
		key.WriteByte(alphabet[n.Int64()])
	}
	return key.String()
}
func (s *runtimeState) validSecret(secret string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.validSecretLocked(secret)
}

func (s *runtimeState) validSecretLocked(secret string) bool {
	for _, candidate := range s.tokens {
		if candidate == secret {
			return true
		}
	}
	return false
}

func (s *runtimeState) connect(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("probe") == "1" {
		s.probeConnection(w, r)
		return
	}
	secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if status, err := s.authorize(r, r.Header.Get("Authorization") == "Bearer "+secret && s.validSecret(secret)); err != nil {
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "300")
		}
		http.Error(w, err.Error(), status)
		return
	}
	if !acceptClientVersion(w, r) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A token may have been reclaimed since the initial authentication check.
	valid := false
	for _, candidate := range s.tokens {
		if candidate == secret {
			valid = true
			break
		}
	}
	if !valid {
		http.Error(w, i18n.Text(i18n.Terminal(), "unauthorized"), http.StatusUnauthorized)
		return
	}
	if !s.multiDevice[secret] && s.hasClientLocked(secret) {
		http.Error(w, i18n.Text(i18n.Terminal(), "a.client.is.already.connected.with.this.token"), http.StatusConflict)
		return
	}
	if owner := s.clientOwners[secret]; owner != nil && s.maxClientsPerIP > 0 && s.onlineForIP(owner.ip) >= s.maxClientsPerIP {
		http.Error(w, i18n.Text(i18n.Terminal(), "client.ip.limit.reached"), http.StatusTooManyRequests)
		return
	}
	w.Header().Set(protocol.HeartbeatHeader, "1")
	conn, err := (&websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}).Upgrade(w, r, w.Header())
	if err != nil {
		return
	}
	id := s.newClientIDLocked()
	c := &client{conn: conn, secret: secret, pending: make(map[string]chan protocol.Message), info: clientInfo{
		ID: id, ConnectedAt: time.Now().UTC(),
		Environment: clientEnvironment{
			Hostname: metadataHeader(r, protocol.HostnameHeader), Platform: metadataHeader(r, protocol.PlatformHeader),
			Arch: metadataHeader(r, protocol.ArchHeader), Shell: metadataHeader(r, protocol.ShellHeader),
			DefaultCwd:      metadataHeader(r, protocol.DefaultCwdHeader),
			ClientVersion:   metadataHeader(r, version.Header),
			SessionControls: sessionControlsHeader(r),
		},
	}}
	if s.clients == nil {
		s.clients = make(map[string]*client)
	}
	s.clients[id] = c
	if owner := s.clientOwners[secret]; owner != nil {
		owner.used = true
		owner.expires = time.Time{}
		if s.maxClientsPerIP > 0 {
			s.trimPending(owner.ip, s.maxClientsPerIP-s.onlineForIP(owner.ip))
		}
	}
	s.notifyClientWaitersLocked(secret)
	c.log = logging.FromContext(r.Context()).With("client_id", id)
	c.logger().Info("client connected", "platform", c.info.Environment.Platform, "client_version", c.info.Environment.ClientVersion)
	go func() {
		var disconnectErr error
		disconnectReason := "read_failed"
		stopHeartbeat := c.startHeartbeat(protocol.HeartbeatInterval, protocol.HeartbeatTimeout)
		defer func() {
			conn.Close()
			stopHeartbeat()
			s.mu.Lock()
			if s.clients[id] == c {
				delete(s.clients, id)
				if owner := s.clientOwners[secret]; owner != nil && !s.hasClientLocked(secret) {
					owner.expires = time.Now().Add(tokenOfflineTTL)
				}
			}
			s.mu.Unlock()
			c.mu.Lock()
			c.closed = true
			for _, ch := range c.pending {
				close(ch)
			}
			c.pending = make(map[string]chan protocol.Message)
			c.mu.Unlock()
			code := 0
			if closeErr, ok := disconnectErr.(*websocket.CloseError); ok {
				code = closeErr.Code
				if code == websocket.CloseNormalClosure || code == websocket.CloseGoingAway {
					disconnectReason = "peer_closed"
				}
			}
			level := slog.LevelWarn
			if disconnectReason == "peer_closed" {
				level = slog.LevelInfo
			}
			c.logger().Log(context.Background(), level, "client disconnected", "duration_ms", time.Since(c.info.ConnectedAt).Milliseconds(),
				"reason", disconnectReason, "close_code", code, "error_kind", logging.ErrorKind(disconnectErr))
		}()
		conn.SetReadLimit(2 << 20)
		for {
			var m protocol.Message
			if err := conn.ReadJSON(&m); err != nil {
				disconnectErr = err
				return
			}
			c.mu.Lock()
			ch := c.pending[m.ID]
			if ch != nil {
				select {
				case ch <- m:
				default:
					disconnectErr = fmt.Errorf("output queue full for command %s", m.ID)
					disconnectReason = "output_queue_full"
					// A peer can choose message IDs; do not log an unvalidated value.
					c.logger().Warn("client output queue full")
					c.mu.Unlock()
					return
				}
			}
			c.mu.Unlock()
		}
	}()
}

func serverText(key string) string { return i18n.Text(i18n.Terminal(), key) }
