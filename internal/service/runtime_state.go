package service

import (
	"agenterm/internal/i18n"
	"agenterm/internal/logging"
	terminal "agenterm/internal/terminalapi"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"agenterm/internal/protocol"
	"github.com/gorilla/websocket"
)

type client struct {
	log        *slog.Logger
	info       clientInfo
	secret     string
	resumeHash [sha256.Size]byte
	conn       *websocket.Conn
	write      sync.Mutex
	mu         sync.Mutex
	pending    map[string]chan protocol.Message
	closed     bool
	closing    bool // guarded by runtimeState.mu; prevents new routing during close
	done       chan struct{}
	latencyMS  int64 // Latest heartbeat RTT; guarded by mu. Zero means unmeasured.
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
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return i18n.NewError("client.disconnected")
	}
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
	enableShortCode bool
	clientIPHeader  string                      // empty uses the directly connected peer IP
	tokens          map[string]string           // full token -> client credential
	clients         map[string]*client          // six-character client ID -> connection
	connecting      map[string]int              // client credential -> handshakes in progress
	stopping        bool                        // guarded by mu
	clientWaiters   map[string]*clientWaitState // client credential -> active connection waits
	multiDevice     map[string]bool             // client credential -> multi-device mode
	mu              sync.Mutex
	limiter         authLimiter
	adminLimiter    authLimiter
	shortCodes      shortCodeStore // code -> connection token and redemption count
	maxClientsPerIP int
	clientOwners    map[string]*clientOwner // client secret -> issuing IP and lifecycle
	nextClientOrder uint64
	p2pRooms        map[string]*p2pRoom
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

// controlSecretLocked authenticates a scoped control credential, never a device token.
func (s *runtimeState) controlSecretLocked(value string) (string, bool) {
	token, key, ok := strings.Cut(value, ":")
	if !ok || key == "" {
		return "", false
	}
	secret, ok := s.tokens[token]
	if !ok || !s.tokenUnexpiredLocked(secret) {
		return "", false
	}
	owner := s.clientOwners[secret]
	if owner == nil {
		return "", false
	}
	hash := sha256.Sum256([]byte(key))
	if subtle.ConstantTimeCompare(hash[:], owner.controlKeyHash[:]) != 1 {
		return "", false
	}
	return secret, true
}

func (s *runtimeState) validSecret(secret string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.validSecretLocked(secret)
}

func (s *runtimeState) tokenUnexpiredLocked(secret string) bool {
	owner := s.clientOwners[secret]
	return owner == nil || owner.expires.IsZero() || time.Now().Before(owner.expires) || s.hasClientLocked(secret)
}

func (s *runtimeState) validSecretLocked(secret string) bool {
	if !s.tokenUnexpiredLocked(secret) {
		return false
	}
	for _, candidate := range s.tokens {
		if candidate == secret {
			return true
		}
	}
	return false
}

// validClientRoute binds the displayed public identifier to the authenticated token.
func (s *runtimeState) validClientRoute(secret, path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for encoded, candidate := range s.tokens {
		if candidate != secret {
			continue
		}
		token, err := protocol.Decode(encoded)
		if err != nil {
			continue
		}
		u, err := url.Parse(token.ConnectionURL())
		if err == nil && u.Path == path {
			return true
		}
	}
	return false
}

func (s *runtimeState) reserveConnection(secret, resume string) (*client, string, *controlError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return nil, "", &controlError{Status: http.StatusServiceUnavailable, Message: http.StatusText(http.StatusServiceUnavailable)}
	}
	// A token may have been reclaimed since the initial authentication check.
	if !s.validSecretLocked(secret) {
		return nil, "", &controlError{Status: http.StatusUnauthorized, Message: serverText("unauthorized")}
	}
	var previous *client
	if s.multiDevice[secret] {
		resume = ""
	} else {
		if s.connecting[secret] > 0 {
			return nil, "", &controlError{Status: http.StatusUnauthorized, Message: serverText("unauthorized")}
		}
		for _, existing := range s.clients {
			if existing.secret == secret {
				previous = existing
				break
			}
		}
		if previous != nil {
			hash := sha256.Sum256([]byte(resume))
			if previous.closing || resume == "" || subtle.ConstantTimeCompare(hash[:], previous.resumeHash[:]) != 1 {
				return nil, "", &controlError{Status: http.StatusUnauthorized, Message: serverText("unauthorized")}
			}
		} else {
			resume = randomBytes(32)
		}
	}
	if owner := s.clientOwners[secret]; previous == nil && owner != nil && s.maxClientsPerIP > 0 {
		if _, occupied := s.slotsForIP(owner.ip); occupied >= s.maxClientsPerIP {
			return nil, "", &controlError{Status: http.StatusTooManyRequests, Message: serverText("client.ip.limit.reached")}
		}
	}
	if s.connecting == nil {
		s.connecting = make(map[string]int)
	}
	s.connecting[secret]++
	return previous, resume, nil
}

func (s *runtimeState) connect(w http.ResponseWriter, r *http.Request) {
	secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if status, err := s.authorize(r, r.Header.Get("Authorization") == "Bearer "+secret && s.validSecret(secret) && s.validClientRoute(secret, r.URL.Path)); err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	if !acceptClientVersion(w, r) {
		return
	}
	s.mu.Lock()
	owner := s.clientOwners[secret]
	temporaryOnly := owner != nil && owner.temporaryOnly
	s.mu.Unlock()
	if temporaryOnly {
		w.Header().Set(protocol.ConnectionModeHeader, protocol.TemporaryConnectionMode)
		if r.Header.Get(protocol.ConnectionModeHeader) != protocol.TemporaryConnectionMode {
			http.Error(w, serverText("connection.mode.temporary.required"), http.StatusForbidden)
			return
		}
	}
	previous, resume, rejection := s.reserveConnection(secret, r.Header.Get(protocol.ResumeCredentialHeader))
	if rejection != nil {
		http.Error(w, rejection.Message, rejection.Status)
		return
	}
	if resume != "" {
		w.Header().Set(protocol.ResumeCredentialHeader, resume)
	}
	w.Header().Set(protocol.HeartbeatHeader, "1")
	w.Header().Set(protocol.ClientAPIHeader, terminal.APIVersion)
	upgrader := websocket.Upgrader{
		HandshakeTimeout: protocol.WriteTimeout,
		CheckOrigin:      func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
	}
	conn, err := upgrader.Upgrade(w, r, w.Header())
	s.mu.Lock()
	s.connecting[secret]--
	if s.connecting[secret] == 0 {
		delete(s.connecting, secret)
	}
	if err != nil {
		s.mu.Unlock()
		return
	}
	// The handshake can overlap revocation, expiry, or an explicit close of the old device.
	if s.stopping {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	if !s.validSecretLocked(secret) || (previous != nil && previous.closing) {
		s.mu.Unlock()
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "registration cancelled"), time.Now().Add(time.Second))
		_ = conn.Close()
		return
	}
	if previous != nil && s.clients[previous.info.ID] == previous {
		previous.closing = true
		previous.mu.Lock()
		previous.closed = true
		previous.mu.Unlock()
		s.closeP2PClientLocked(previous, "client reconnected")
		delete(s.clients, previous.info.ID)
	}
	id := s.newClientIDLocked()
	c := &client{conn: conn, secret: secret, resumeHash: sha256.Sum256([]byte(resume)), done: make(chan struct{}), pending: make(map[string]chan protocol.Message), info: clientInfo{
		ID: id, ConnectedAt: time.Now().UTC(),
		Environment: clientEnvironment{
			Hostname: metadataHeader(r, protocol.HostnameHeader), Platform: metadataHeader(r, protocol.PlatformHeader),
			Arch: metadataHeader(r, protocol.ArchHeader), Shell: metadataHeader(r, protocol.ShellHeader),
			DefaultCwd:      metadataHeader(r, protocol.DefaultCwdHeader),
			ClientVersion:   clientReleaseVersion(r),
			SessionControls: sessionControlsHeader(r),
			TerminalAPI:     r.Header.Get(protocol.ClientAPIHeader) == terminal.APIVersion,
			P2P:             !temporaryOnly && r.Header.Get(protocol.P2PCapabilityHeader) == "1",
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
			online, _ := s.slotsForIP(owner.ip)
			s.trimPending(owner.ip, s.maxClientsPerIP-online)
		}
	}
	s.notifyClientWaitersLocked(secret)
	c.log = logging.FromContext(r.Context()).With("client_id", id)
	s.mu.Unlock()
	if previous != nil {
		_ = previous.conn.Close()
	}
	c.logger().Info("client connected", "platform", c.info.Environment.Platform, "client_version", c.info.Environment.ClientVersion)
	go func() {
		var disconnectErr error
		disconnectReason := "read_failed"
		stopHeartbeat := c.startHeartbeat(protocol.HeartbeatInterval, protocol.HeartbeatTimeout)
		defer func() {
			defer close(c.done)
			conn.Close()
			stopHeartbeat()
			s.mu.Lock()
			s.closeP2PClientLocked(c, "client disconnected")

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

			if m.Type == "p2p_done" {
				s.mu.Lock()
				s.p2pCommandDoneLocked(c, m)
				s.mu.Unlock()
			}
			c.mu.Lock()
			ch := c.pending[m.ID]
			if ch != nil {
				select {
				case ch <- m:
				default:
					disconnectErr = fmt.Errorf("response queue full for request %s", m.ID)
					disconnectReason = "response_queue_full"
					// A peer can choose message IDs; do not log an unvalidated value.
					c.logger().Warn("client response queue full")
					c.mu.Unlock()
					return
				}
			}
			c.mu.Unlock()
		}
	}()
}

func serverText(key string) string { return i18n.Text(i18n.Terminal(), key) }
