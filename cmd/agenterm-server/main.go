package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"agenterm/internal/protocol"
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
	tokens      map[string]string  // full token -> client credential
	clients     map[string]*client // client credential -> connection
	waiters     map[string]map[chan struct{}]struct{}
	mu          sync.Mutex
	sessions    map[string]*session
	limiter     authLimiter
	invitations map[string]invitation
}

type invitation struct {
	token   string
	expires time.Time
}

func (s *server) redeemInvitation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
func (s *server) validSecret(secret string) bool {
	for _, candidate := range s.tokens {
		if candidate == secret {
			return true
		}
	}
	return false
}

func (s *server) connect(w http.ResponseWriter, r *http.Request) {
	secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if status, err := s.authorize(r.RemoteAddr, r.Header.Get("Authorization") == "Bearer "+secret && s.validSecret(secret)); err != nil {
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "300")
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients[secret] != nil {
		http.Error(w, "a client is already connected with this token", http.StatusConflict)
		return
	}
	conn, err := (&websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{conn: conn, pending: make(map[string]chan protocol.Message)}
	s.clients[secret] = c
	for ch := range s.waiters[secret] {
		close(ch)
	}
	delete(s.waiters, secret)
	log.Println("client connected")
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
			log.Println("client disconnected")
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
					log.Printf("output queue full for %s; closing connection", m.ID)
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
		return "", fmt.Errorf("no client connected")
	}
	id := random()
	ch := make(chan protocol.Message, 256)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return "", fmt.Errorf("client disconnected")
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
				return "", fmt.Errorf("client disconnected")
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
					result += "\n[output truncated at 1 MiB]"
				}
				if m.Error != "" {
					return result, fmt.Errorf("%s", m.Error)
				}
				return fmt.Sprintf("%s\n[exit code: %d]", result, m.ExitCode), nil
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
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	header := r.Header.Get("Authorization")
	token := strings.TrimPrefix(header, "Bearer ")
	secret, ok := s.tokens[token]
	if status, err := s.authorize(r.RemoteAddr, header == "Bearer "+token && ok); err != nil {
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
		respond(w, json.RawMessage("null"), nil, map[string]any{"code": -32700, "message": "parse error"})
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
		version := p.ProtocolVersion
		if version != "2025-03-26" && version != "2025-06-18" {
			version = "2025-03-26"
		}
		respond(w, req.ID, map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "agenterm", "version": "0.1.8"}}, nil)
	case "ping":
		respond(w, req.ID, map[string]any{}, nil)
	case "tools/list":
		tools := []any{
			tool("wait_for_client", "After sharing the invitation or token, call this tool to wait for the client associated with this MCP connection. Returns connected or timeout; call again after a timeout if appropriate.", map[string]any{"timeout_ms": map[string]any{"type": "integer", "minimum": 0, "maximum": 300000, "description": "Maximum wait in milliseconds (default 30000; 0 checks immediately)"}}, []string{}),
			tool("execute_command", "Execute a non-interactive command and return output when it finishes.", map[string]any{"command": stringField("Shell command")}, []string{"command"}),
			tool("session_start", "Start an interactive terminal session. Then use session_read to see output, session_write to answer prompts, and session_stop to stop it.", map[string]any{"command": stringField("Shell command to run")}, []string{"command"}),
			tool("session_read", "Read new output while the session is running. Supply next_cursor from the previous read; wait_ms can long-poll for output.", map[string]any{"session_id": stringField("Session identifier"), "cursor": map[string]any{"type": "integer", "minimum": 0, "description": "Byte cursor, initially 0"}, "wait_ms": map[string]any{"type": "integer", "minimum": 0, "maximum": 20000, "description": "Wait up to this many milliseconds for output"}}, []string{"session_id"}),
			tool("session_write", "Send input to a running session. Include a newline to answer a prompt; input may be echoed in the terminal output.", map[string]any{"session_id": stringField("Session identifier"), "input": stringField("Input bytes as text, e.g. yes\\n")}, []string{"session_id", "input"}),
			tool("session_stop", "Stop a running session.", map[string]any{"session_id": stringField("Session identifier")}, []string{"session_id"}),
		}
		respond(w, req.ID, map[string]any{"tools": tools}, nil)
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			respond(w, req.ID, nil, map[string]any{"code": -32602, "message": "invalid arguments"})
			return
		}
		var a struct {
			Command   string `json:"command"`
			SessionID string `json:"session_id"`
			Input     string `json:"input"`
			Cursor    int64  `json:"cursor"`
			WaitMS    int    `json:"wait_ms"`
			TimeoutMS *int   `json:"timeout_ms"`
		}
		if err := json.Unmarshal(p.Arguments, &a); err != nil {
			respond(w, req.ID, nil, map[string]any{"code": -32602, "message": "invalid arguments"})
			return
		}
		var result any
		var err error
		switch p.Name {
		case "wait_for_client":
			timeout := 30000
			if a.TimeoutMS != nil {
				timeout = *a.TimeoutMS
			}
			if timeout < 0 || timeout > 300000 {
				err = fmt.Errorf("timeout_ms must be between 0 and 300000")
			} else {
				var status string
				status, err = s.waitForClient(r.Context(), secret, time.Duration(timeout)*time.Millisecond)
				if err == nil {
					result = map[string]any{"status": status}
				}
			}
		case "execute_command":
			if strings.TrimSpace(a.Command) == "" {
				err = fmt.Errorf("command is required")
			} else {
				result, err = s.execute(r.Context(), secret, a.Command)
			}
		case "session_start":
			if strings.TrimSpace(a.Command) == "" {
				err = fmt.Errorf("command is required")
			} else {
				var sess *session
				sess, err = s.startSession(secret, a.Command)
				if err == nil {
					result = map[string]any{"session_id": sess.id, "cursor": 0}
				}
			}
		case "session_read":
			if a.Cursor < 0 || a.WaitMS < 0 || a.WaitMS > 20000 {
				err = fmt.Errorf("invalid cursor or wait_ms")
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
			err = fmt.Errorf("unknown tool: %s", p.Name)
		}
		if err != nil {
			result = map[string]any{"error": err.Error()}
		}
		b, _ := json.Marshal(result)
		respond(w, req.ID, map[string]any{"content": []any{map[string]string{"type": "text", "text": string(b)}}, "isError": err != nil}, nil)
	default:
		respond(w, req.ID, nil, map[string]any{"code": -32601, "message": "method not found"})
	}
}
func stringField(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
func tool(name, description string, properties map[string]any, required []string) map[string]any {
	return map[string]any{"name": name, "description": description, "inputSchema": map[string]any{"type": "object", "properties": properties, "required": required}}
}
func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	count := flag.Int("clients", 5, "number of client connection tokens to issue")
	public := flag.String("public-url", "http://127.0.0.1:8080", "Server base URL reachable by client")
	flag.Parse()
	u, err := url.Parse(*public)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		log.Fatal("public-url must be an http(s) origin without a path")
	}
	securePublicURL := u.Scheme == "https"
	if securePublicURL {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/client"
	if *count < 1 || *count > 1000 {
		log.Fatal("clients must be between 1 and 1000")
	}
	s := &server{tokens: make(map[string]string), clients: make(map[string]*client), invitations: make(map[string]invitation)}
	var issuedTokens []string
	for i := 1; i <= *count; i++ {
		secret := randomBytes(protocol.TokenSecretSize)
		token, err := protocol.Encode(protocol.Token{URL: u.String(), Secret: secret})
		if err != nil {
			log.Fatal(err)
		}
		s.tokens[token] = secret
		issuedTokens = append(issuedTokens, token)
		fmt.Printf("Agenterm token %d:\n%s\n\n", i, token)
	}
	issueInvitations := func() {
		type output struct {
			value string
			index int
		}
		var outputs []output
		s.mu.Lock()
		s.invitations = make(map[string]invitation)
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
		fmt.Println("Press Enter to replace all unredeemed invitations with fresh ones.")
	}
	issueInvitations()
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			issueInvitations()
		}
	}()
	fmt.Printf("MCP endpoint: %s/mcp\n", strings.TrimRight(*public, "/"))
	if !securePublicURL {
		log.Print("WARNING: HTTP and WebSocket traffic is unencrypted; tokens, commands and output can be intercepted. Use HTTPS with a reverse proxy or a trusted network.")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/client", s.connect)
	mux.HandleFunc("/invite/redeem", s.redeemInvitation)
	mux.HandleFunc("/mcp", s.mcp)
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
