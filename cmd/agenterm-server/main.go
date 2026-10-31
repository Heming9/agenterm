package main

import (
	"agenterm/internal/i18n"
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
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
	"agenterm/internal/version"
	"github.com/gorilla/websocket"
)

type client struct {
	conn    *websocket.Conn
	write   sync.Mutex
	mu      sync.Mutex
	pending map[string]chan protocol.Message
	closed  bool
}

func (c *client) send(m protocol.Message) error {
	c.write.Lock()
	defer c.write.Unlock()
	return c.conn.WriteJSON(m)
}

type server struct {
	adminKey        string
	noPassword      bool
	clientURL       string
	tokens          map[string]string  // full token -> client credential
	clients         map[string]*client // client credential -> connection
	waiters         map[string]map[chan struct{}]struct{}
	mu              sync.Mutex
	sessions        map[string]*session
	limiter         authLimiter
	invitations     map[string]invitation
	maxClientsPerIP int
	clientOwners    map[string]*clientOwner // client secret -> issuing IP and lifecycle
	nextClientOrder uint64
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
	if status, err := s.authorize(r.RemoteAddr, ok); err != nil {
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
	delete(s.invitations, secret)
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
	if status, err := s.authorize(r.RemoteAddr, r.Header.Get("Authorization") == "Bearer "+secret && s.validSecret(secret)); err != nil {
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
	if s.clients[secret] != nil {
		http.Error(w, i18n.Text(i18n.Terminal(), "a.client.is.already.connected.with.this.token"), http.StatusConflict)
		return
	}
	if owner := s.clientOwners[secret]; owner != nil && s.maxClientsPerIP > 0 && s.onlineForIP(owner.ip) >= s.maxClientsPerIP {
		http.Error(w, i18n.Text(i18n.Terminal(), "client.ip.limit.reached"), http.StatusTooManyRequests)
		return
	}
	conn, err := (&websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}).Upgrade(w, r, w.Header())
	if err != nil {
		return
	}
	c := &client{conn: conn, pending: make(map[string]chan protocol.Message)}
	s.clients[secret] = c
	if owner := s.clientOwners[secret]; owner != nil {
		owner.used = true
		s.trimPending(owner.ip, s.maxClientsPerIP-s.onlineForIP(owner.ip))
	}
	for ch := range s.waiters[secret] {
		close(ch)
	}
	delete(s.waiters, secret)
	log.Println(serverText("client.connected"))
	go func() {
		defer func() {
			conn.Close()
			s.mu.Lock()
			if s.clients[secret] == c {
				delete(s.clients, secret)
			}
			s.mu.Unlock()
			c.mu.Lock()
			c.closed = true
			for _, ch := range c.pending {
				close(ch)
			}
			c.pending = make(map[string]chan protocol.Message)
			c.mu.Unlock()
			log.Println(serverText("client.disconnected"))
		}()
		conn.SetReadLimit(2 << 20)
		for {
			var m protocol.Message
			if err := conn.ReadJSON(&m); err != nil {
				return
			}
			c.mu.Lock()
			ch := c.pending[m.ID]
			if ch != nil {
				select {
				case ch <- m:
				default:
					log.Printf(serverText("output.queue.full.for.s.closing.connection"), m.ID)
					c.mu.Unlock()
					return
				}
			}
			c.mu.Unlock()
		}
	}()
}

// waitForClient checks and subscribes under the same lock so a connection
// cannot arrive between the two operations and go unnoticed.
func (s *server) waitForClient(ctx context.Context, secret string, timeout time.Duration) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		if s.clients[secret] != nil {
			s.mu.Unlock()
			return "connected", nil
		}
		ch := make(chan struct{})
		if s.waiters == nil {
			s.waiters = make(map[string]map[chan struct{}]struct{})
		}
		if s.waiters[secret] == nil {
			s.waiters[secret] = make(map[chan struct{}]struct{})
		}
		s.waiters[secret][ch] = struct{}{}
		s.mu.Unlock()

		var status string
		var err error
		select {
		case <-ch:
		case <-timer.C:
			status = "timeout"
		case <-ctx.Done():
			err = ctx.Err()
		}
		s.mu.Lock()
		delete(s.waiters[secret], ch)
		if len(s.waiters[secret]) == 0 {
			delete(s.waiters, secret)
		}
		s.mu.Unlock()
		if status != "" || err != nil {
			return status, err
		}
	}
}
func (s *server) execute(ctx context.Context, secret, command string) (string, error) {
	s.mu.Lock()
	c := s.clients[secret]
	s.mu.Unlock()
	if c == nil {
		return "", i18n.NewError("no.client.connected")
	}
	id := random()
	ch := make(chan protocol.Message, 256)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return "", i18n.NewError("client.disconnected")
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.send(protocol.Message{Type: "exec", ID: id, Command: command}); err != nil {
		return "", err
	}
	var out strings.Builder
	truncated := false
	for {
		select {
		case <-ctx.Done():
			_ = c.send(protocol.Message{Type: "cancel", ID: id})
			return "", ctx.Err()
		case m, ok := <-ch:
			if !ok {
				return "", i18n.NewError("client.disconnected")
			}
			switch m.Type {
			case "output":
				if out.Len()+len(m.Output) <= 1<<20 {
					out.WriteString(m.Output)
				} else {
					truncated = true
				}
			case "done":
				result := out.String()
				if truncated {
					result += serverText("mcp.output.truncated.at.1.mib")
				}
				if m.Error != "" {
					return result, fmt.Errorf("%s", m.Error)
				}
				return fmt.Sprintf(i18n.Text(i18n.Terminal(), "s.exit.code.d"), result, m.ExitCode), nil
			}
		}
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func respond(w http.ResponseWriter, id json.RawMessage, result any, rpcErr any) {
	w.Header().Set("Content-Type", "application/json")
	payload := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		payload["error"] = rpcErr
	} else {
		payload["result"] = result
	}
	_ = json.NewEncoder(w).Encode(payload)
}
func (s *server) mcp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, i18n.Text(i18n.Terminal(), "method.not.allowed"), http.StatusMethodNotAllowed)
		return
	}
	header := r.Header.Get("Authorization")
	token := strings.TrimPrefix(header, "Bearer ")
	s.mu.Lock()
	secret, ok := s.tokens[token]
	s.mu.Unlock()
	admin := (s.noPassword && header == "") || (s.adminKey != "" && header == "Bearer "+token && subtle.ConstantTimeCompare([]byte(token), []byte(s.adminKey)) == 1)
	if status, err := s.authorize(r.RemoteAddr, admin || (header == "Bearer "+token && ok)); err != nil {
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "300")
		} else {
			w.Header().Set("WWW-Authenticate", `Bearer realm="agenterm"`)
		}
		http.Error(w, err.Error(), status)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		respond(w, json.RawMessage("null"), nil, map[string]any{"code": -32700, "message": serverText("mcp.parse.error")})
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		mcpVersion := p.ProtocolVersion
		if mcpVersion != "2025-03-26" && mcpVersion != "2025-06-18" {
			mcpVersion = "2025-03-26"
		}
		respond(w, req.ID, map[string]any{"protocolVersion": mcpVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "agenterm", "version": version.Current}}, nil)
	case "ping":
		respond(w, req.ID, map[string]any{}, nil)
	case "tools/list":
		tools := []any{
			tool("wait_for_client", serverText("mcp.after.sharing.the.invitation.or.token.call.this"), map[string]any{"timeout_ms": map[string]any{"type": "integer", "minimum": 0, "maximum": 300000, "description": serverText("mcp.maximum.wait.in.milliseconds.default.30000.0.checks")}}, []string{}),
			tool("execute_command", serverText("mcp.execute.a.non.interactive.command.and.return.output"), map[string]any{"command": stringField(serverText("mcp.shell.command"))}, []string{"command"}),
			tool("session_start", serverText("mcp.start.an.interactive.terminal.session.then.use.session"), map[string]any{"command": stringField(serverText("mcp.shell.command.to.run"))}, []string{"command"}),
			tool("session_read", serverText("mcp.read.new.output.while.the.session.is.running"), map[string]any{"session_id": stringField(serverText("mcp.session.identifier")), "cursor": map[string]any{"type": "integer", "minimum": 0, "description": serverText("mcp.byte.cursor.initially.0")}, "wait_ms": map[string]any{"type": "integer", "minimum": 0, "maximum": 20000, "description": serverText("mcp.wait.up.to.this.many.milliseconds.for.output")}}, []string{"session_id"}),
			tool("session_write", serverText("mcp.send.input.to.a.running.session.include.a"), map[string]any{"session_id": stringField(serverText("mcp.session.identifier")), "input": stringField(serverText("mcp.input.bytes.as.text.e.g.yes.n"))}, []string{"session_id", "input"}),
			tool("session_stop", serverText("mcp.stop.a.running.session"), map[string]any{"session_id": stringField(serverText("mcp.session.identifier"))}, []string{"session_id"}),
		}
		if admin {
			tools = append(tools, tool("create_client", serverText("mcp.create.a.client.connection.token.and.a.one"), map[string]any{}, []string{}))
			for _, item := range tools[:len(tools)-1] {
				t := item.(map[string]any)
				schema := t["inputSchema"].(map[string]any)
				schema["properties"].(map[string]any)["client_token"] = stringField(serverText("mcp.target.client.connection.token.required.when.authenticated.with"))
				schema["required"] = append(schema["required"].([]string), "client_token")
			}
		}
		respond(w, req.ID, map[string]any{"tools": tools}, nil)
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			respond(w, req.ID, nil, map[string]any{"code": -32602, "message": serverText("mcp.invalid.arguments")})
			return
		}
		var a struct {
			ClientToken string `json:"client_token"`
			Command     string `json:"command"`
			SessionID   string `json:"session_id"`
			Input       string `json:"input"`
			Cursor      int64  `json:"cursor"`
			WaitMS      int    `json:"wait_ms"`
			TimeoutMS   *int   `json:"timeout_ms"`
		}
		if err := json.Unmarshal(p.Arguments, &a); err != nil {
			respond(w, req.ID, nil, map[string]any{"code": -32602, "message": serverText("mcp.invalid.arguments")})
			return
		}
		var result any
		var err error
		if p.Name == "create_client" && !admin {
			err = i18n.NewError("management.key.required")
		} else if admin && p.Name != "create_client" {
			s.mu.Lock()
			secret, ok = s.tokens[a.ClientToken]
			s.mu.Unlock()
			if !ok {
				err = i18n.NewError("valid.client.token.is.required")
			}
		}
		if err == nil {
			switch p.Name {
			case "create_client":
				var owner string
				if s.maxClientsPerIP > 0 {
					owner, err = s.requestIP(r)
				}
				if err == nil {
					result, err = s.createClient(owner)
				}

			case "wait_for_client":
				timeout := 90000
				if a.TimeoutMS != nil {
					timeout = *a.TimeoutMS
				}
				if timeout < 0 || timeout > 300000 {
					err = i18n.NewError("timeout.ms.must.be.between.0.and.300000")
				} else {
					var status string
					status, err = s.waitForClient(r.Context(), secret, time.Duration(timeout)*time.Millisecond)
					if err == nil {
						result = map[string]any{"status": status}
					}
				}
			case "execute_command":
				if strings.TrimSpace(a.Command) == "" {
					err = i18n.NewError("command.is.required")
				} else {
					result, err = s.execute(r.Context(), secret, a.Command)
				}
			case "session_start":
				if strings.TrimSpace(a.Command) == "" {
					err = i18n.NewError("command.is.required")
				} else {
					var sess *session
					sess, err = s.startSession(secret, a.Command)
					if err == nil {
						result = map[string]any{"session_id": sess.id, "cursor": 0}
					}
				}
			case "session_read":
				if a.Cursor < 0 || a.WaitMS < 0 || a.WaitMS > 20000 {
					err = i18n.NewError("invalid.cursor.or.wait.ms")
				} else {
					var sess *session
					sess, err = s.getSession(secret, a.SessionID)
					if err == nil {
						result, err = sess.read(r.Context(), a.Cursor, time.Duration(a.WaitMS)*time.Millisecond)
					}
				}
			case "session_write", "session_stop":
				var sess *session
				sess, err = s.getSession(secret, a.SessionID)
				if err == nil {
					typ := "input"
					if p.Name == "session_stop" {
						typ = "cancel"
					}
					err = sess.send(typ, a.Input)
					if err == nil {
						result = map[string]any{"accepted": true}
					}
				}
			default:
				err = i18n.NewError("unknown.tool.s", p.Name)
			}
		}
		if err != nil {
			result = map[string]any{"error": err.Error()}
		}
		b, _ := json.Marshal(result)
		respond(w, req.ID, map[string]any{"content": []any{map[string]string{"type": "text", "text": string(b)}}, "isError": err != nil}, nil)
	default:
		respond(w, req.ID, nil, map[string]any{"code": -32601, "message": serverText("mcp.method.not.found")})
	}
}
func stringField(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
func tool(name, description string, properties map[string]any, required []string) map[string]any {
	return map[string]any{"name": name, "description": description, "inputSchema": map[string]any{"type": "object", "properties": properties, "required": required}}
}
func serverText(key string) string { return i18n.Text(i18n.Terminal(), key) }

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", serverText("http.listen.address"))
	passwordFlag := flag.String("passwd", "", serverText("mcp.management.key.generated.when.omitted"))
	noPassword := flag.Bool("no-password", false, serverText("allow.mcp.management.access.without.a.password"))
	autoCreate := flag.Bool("generate", false, serverText("generate.a.client.token.and.invitation.at"))
	public := flag.String("public-url", "http://127.0.0.1:8080", serverText("public.server.url.optional.agenterm.suffix"))
	maxClientsPerIP := flag.Int("max-clients-per-ip", 0, serverText("max.clients.per.ip"))
	flag.Parse()
	if *maxClientsPerIP < 0 {
		log.Fatal(serverText("max.clients.per.ip.invalid"))
	}
	u, err := parsePublicURL(*public)
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
	u.Path = protocol.ClientPath
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
	s := &server{adminKey: adminKey, noPassword: *noPassword, clientURL: u.String(), tokens: make(map[string]string), clients: make(map[string]*client), invitations: make(map[string]invitation), maxClientsPerIP: *maxClientsPerIP}
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
					delete(s.invitations, key)
					break
				}
			}
		}
		for i, token := range issuedTokens {
			secret := randomBytes(protocol.TokenSecretSize)
			value, err := protocol.Encode(protocol.Token{URL: u.String(), Secret: secret})
			if err != nil {
				s.mu.Unlock()
				log.Fatal(err)
			}
			s.invitations[secret] = invitation{token: token, expires: time.Now().Add(15 * time.Minute)}
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
	srv := &http.Server{Addr: *listen, Handler: s.routes(releaseTag, assets), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
