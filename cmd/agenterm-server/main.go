package main

import (
	"agenterm/internal/i18n"
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"agenterm/internal/installer"
	"agenterm/internal/protocol"
	"github.com/gorilla/websocket"
)

type client struct {
	info    clientInfo
	secret  string
	conn    *websocket.Conn
	write   sync.Mutex
	mu      sync.Mutex
	pending map[string]chan protocol.Message
	closed  bool
}

func (c *client) send(m protocol.Message) error {
	c.write.Lock()
	defer c.write.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(protocol.WriteTimeout)); err != nil {
		return err
	}
	return c.conn.WriteJSON(m)
}

type server struct {
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
	limiter         authLimiter
	invitations     map[string]invitation
	installCodes    map[string]string // six-character installer code -> invitation secret
	maxClientsPerIP int
	clientOwners    map[string]*clientOwner // client secret -> issuing IP and lifecycle
	nextClientOrder uint64
	mcpOnce         sync.Once
	mcpHandler      http.Handler
}

type invitation struct {
	token   string
	expires time.Time
}

func (s *server) redeemInvitation(w http.ResponseWriter, r *http.Request) {
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
func (s *server) validSecret(secret string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.validSecretLocked(secret)
}

func (s *server) validSecretLocked(secret string) bool {
	for _, candidate := range s.tokens {
		if candidate == secret {
			return true
		}
	}
	return false
}

func (s *server) connect(w http.ResponseWriter, r *http.Request) {
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
		ID: id, Hostname: metadataHeader(r, protocol.HostnameHeader), Platform: metadataHeader(r, protocol.PlatformHeader), ConnectedAt: time.Now().UTC(),
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
	log.Printf("client connected id=%s hostname=%q platform=%q", id, c.info.Hostname, c.info.Platform)
	go func() {
		var disconnectErr error
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
			log.Printf("client disconnected id=%s hostname=%q duration=%s reason=%q", id, c.info.Hostname, time.Since(c.info.ConnectedAt).Round(time.Millisecond), disconnectErr)
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
					log.Printf(serverText("output.queue.full.for.s.closing.connection"), m.ID)
					c.mu.Unlock()
					return
				}
			}
			c.mu.Unlock()
		}
	}()
}

func serverText(key string) string { return i18n.Text(i18n.Terminal(), key) }

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", serverText("http.listen.address"))
	passwordFlag := flag.String("passwd", "", serverText("mcp.management.key.generated.when.omitted"))
	noPassword := flag.Bool("no-password", false, serverText("allow.mcp.management.access.without.a.password"))
	autoCreate := flag.Bool("generate", false, serverText("generate.a.client.token.and.invitation.at"))
	public := flag.String("public-url", "http://127.0.0.1:8080", serverText("public.server.url.optional.agenterm.suffix"))
	urlPrefix := flag.String("url-prefix", protocol.PathPrefix, serverText("server.url.prefix"))
	maxClientsPerIP := flag.Int("max-clients-per-ip", 0, serverText("max.clients.per.ip"))
	clientIPHeader := flag.String("client-ip-header", "", serverText("client.ip.header"))
	flag.Parse()
	if *maxClientsPerIP < 0 {
		log.Fatal(serverText("max.clients.per.ip.invalid"))
	}
	u, err := parsePublicURL(*public, *urlPrefix)
	if err != nil {
		log.Fatal(err)
	}
	publicBase := u.String()
	securePublicURL := u.Scheme == "https"
	if securePublicURL {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path += "/client"
	keySpecified := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "passwd" {
			keySpecified = true
		}
	})
	if keySpecified && (*passwordFlag == "" || *noPassword) {
		log.Fatal(serverText("passwd.must.be.nonempty.and.cannot.be"))
	}
	// Prepare downloads before printing invitations or accepting requests, so a
	// first installer request never waits for decompression or archive generation.
	assets, err := installer.Assets()
	if err != nil {
		log.Fatal(err)
	}
	adminKey := *passwordFlag
	if !*noPassword && !keySpecified {
		adminKey = randomManagementKey()
		fmt.Printf(serverText("mcp.management.key.save.securely.s"), adminKey)
	}
	s := &server{adminKey: adminKey, noPassword: *noPassword, clientURL: u.String(), clientIPHeader: http.CanonicalHeaderKey(*clientIPHeader), tokens: make(map[string]string), clients: make(map[string]*client), invitations: make(map[string]invitation), maxClientsPerIP: *maxClientsPerIP}
	if *noPassword {
		log.Print(serverText("warning.mcp.management.access.has.no.password"))
	}
	var issuedTokens []string
	if *autoCreate {
		secret := randomBytes(protocol.TokenSecretSize)
		token, err := protocol.Encode(protocol.Token{URL: u.String(), Secret: secret})
		if err != nil {
			log.Fatal(err)
		}
		s.tokens[token] = secret
		issuedTokens = append(issuedTokens, token)
		fmt.Printf(serverText("agenterm.token.1.s"), token)
	}
	issueInvitations := func() {
		type output struct {
			value string
			index int
		}
		var outputs []output
		s.mu.Lock()
		for key, entry := range s.invitations {
			for _, token := range issuedTokens {
				if entry.token == token {
					s.deleteInvitation(key)
					break
				}
			}
		}
		for i, token := range issuedTokens {
			value, err := s.newInvitation(token, time.Now().Add(15*time.Minute))
			if err != nil {
				s.mu.Unlock()
				log.Fatal(err)
			}
			outputs = append(outputs, output{value: value, index: i + 1})
		}
		s.mu.Unlock()
		for _, out := range outputs {
			printInstallCommands(out.index, out.value)
		}
		fmt.Println(serverText("press.enter.to.replace.unredeemed.startup.invitations"))
	}
	if *autoCreate {
		issueInvitations()
	}
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			if *autoCreate {
				issueInvitations()
			}
		}
	}()
	fmt.Printf(serverText("mcp.endpoint.s.mcp"), publicBase)
	if !securePublicURL {
		log.Print(serverText("warning.http.and.websocket.traffic.is.unencrypted"))
	}
	srv := &http.Server{Addr: *listen, Handler: s.routes(downloadTag, assets), ReadHeaderTimeout: 10 * time.Second}
	go s.cleanupLoop(context.Background(), time.Minute)
	log.Fatal(srv.ListenAndServe())
}
