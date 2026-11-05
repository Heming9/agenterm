package client

import (
	"agenterm/internal/i18n"
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/gorilla/websocket"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Callbacks may be invoked concurrently by running commands. When Command is
// supplied, it replaces the plain-text command headers and footers in logWriter;
// Output, when supplied, receives process output with its command ID; otherwise
// process output is written to logWriter as it arrives.
type Callbacks struct {
	IdleTimeout      time.Duration
	IdleDisconnected func()
	idle             *idleTracker
	// Redeemed receives the reusable token after an invitation is exchanged.
	Redeemed     func(token string)
	Connecting   func()
	Connected    func()
	Reconnecting func(ReconnectEvent)
	Latency      func(time.Duration)
	Command      func(CommandEvent)
	Output       func(id string, data []byte)
}

type connection struct {
	ws         *websocket.Conn
	write      sync.Mutex
	mu         sync.Mutex
	api        API
	readers    map[string]context.CancelFunc
	bootstraps map[string]context.CancelFunc
	log        io.Writer
	command    func(CommandEvent)
	output     func(string, []byte)
	workers    sync.WaitGroup
	outputMu   sync.Mutex
}

func (c *connection) send(m protocol.Message) error {
	c.write.Lock()
	defer c.write.Unlock()
	if err := c.ws.SetWriteDeadline(time.Now().Add(protocol.WriteTimeout)); err != nil {
		return err
	}
	return c.ws.WriteJSON(m)
}

// TokenValidationError gives users a consistent message for malformed or empty credentials.
func TokenValidationError() error {
	return i18n.NewError("invalid.agenterm.connection.address.or.token")
}

// RunWithCallbacks runs a connection with structured lifecycle notifications for
// consumers such as the desktop UI, retaining the local lock across retries.
func RunWithCallbacks(ctx context.Context, token string, logWriter io.Writer, callbacks Callbacks) error {
	return runWithCallbacks(ctx, token, false, logWriter, callbacks)
}

// RunInvitationWithCallbacks reserves this user's connection to the server
// before redeeming the invitation, holding the reservation until disconnection.
// The caller must obtain user consent before calling this function.
func RunInvitationWithCallbacks(ctx context.Context, invitation string, logWriter io.Writer, callbacks Callbacks) error {
	return runWithCallbacks(ctx, invitation, true, logWriter, callbacks)
}

func runWithCallbacks(ctx context.Context, token string, invitation bool, logWriter io.Writer, callbacks Callbacks) error {
	address, _, err := ConnectionAddress(token)
	if err != nil {
		return err
	}
	lock, err := acquireConnectionLock(address)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	var t protocol.Token
	if shortURL, ok := ShortCodeURL(token); ok && !invitation {
		var resolved string
		t, resolved, err = redeemShortCode(ctx, shortURL)
		if err != nil {
			return err
		}
		if callbacks.Redeemed != nil {
			callbacks.Redeemed(resolved)
		}
	} else {
		if invitation {
			token, err = invite.Redeem(ctx, token)
			if err != nil {
				return err
			}
			if callbacks.Redeemed != nil {
				callbacks.Redeemed(token)
			}
		}
		t, err = protocol.Decode(token)
		if err != nil {
			return TokenValidationError()
		}
	}
	selectedKey, err := connectionLockKey(address)
	if err != nil {
		return err
	}
	targetKey, err := connectionLockKey(t.URL)
	if err != nil {
		return err
	}
	if selectedKey != targetKey {
		targetLock, err := acquireConnectionLock(t.URL)
		if err != nil {
			return err
		}
		defer targetLock.Close()
		lock.Close()
	}
	return reconnect(ctx, t, logWriter, callbacks)
}

func connectOnce(ctx context.Context, t protocol.Token, logWriter io.Writer, callbacks Callbacks, api API) (result attemptResult) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+t.Secret)
	headers.Set(version.Header, version.Current)
	hostname, _ := os.Hostname()
	headers.Set(protocol.HostnameHeader, base64.RawURLEncoding.EncodeToString([]byte(hostname)))
	headers.Set(protocol.PlatformHeader, runtime.GOOS)
	headers.Set(protocol.ArchHeader, runtime.GOARCH)
	headers.Set(protocol.SessionControlsHeader, strings.Join(Controls(), ","))
	headers.Set(protocol.P2PCapabilityHeader, "1")
	headers.Set(protocol.ClientAPIHeader, APIVersion)
	headers.Set(protocol.ShellHeader, base64.RawURLEncoding.EncodeToString([]byte(Shell())))
	cwd, _ := os.Getwd()
	headers.Set(protocol.DefaultCwdHeader, base64.RawURLEncoding.EncodeToString([]byte(cwd)))
	ws, resp, err := websocket.DefaultDialer.DialContext(ctx, t.ConnectionURL(), headers)
	if err != nil {
		if resp != nil {
			if resp.Body != nil {
				resp.Body.Close()
			}
			if resp.StatusCode == http.StatusUpgradeRequired {
				if versionErr := version.Check(version.Current, resp.Header.Get(version.Header)); versionErr != nil {
					return attemptResult{err: versionErr, permanent: true}
				}
			}
			if resp.StatusCode == http.StatusUnauthorized {
				return attemptResult{err: i18n.NewError("invalid.or.expired.token"), permanent: true}
			}
			return attemptResult{err: i18n.NewError("connect.w.http.d", err, resp.StatusCode), permanent: !retryHTTPStatus(resp.StatusCode), retryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now())}
		}
		return attemptResult{err: err}
	}
	defer ws.Close()
	if err := version.Check(version.Current, resp.Header.Get(version.Header)); err != nil {
		return attemptResult{err: err, permanent: true}
	}
	if resp.Header.Get(protocol.ClientAPIHeader) != APIVersion {
		return attemptResult{err: i18n.NewError("client.api.required"), permanent: true}
	}
	c := &connection{ws: ws, api: api, readers: make(map[string]context.CancelFunc), bootstraps: make(map[string]context.CancelFunc), log: logWriter, command: callbacks.Command, output: callbacks.Output}
	defer func() {
		// Cancel transport waiters and bootstrap helpers. Terminal ownership
		// remains with the manager across reconnections.
		ws.Close()
		c.mu.Lock()
		for _, cancel := range c.readers {
			cancel()
		}
		for _, cancel := range c.bootstraps {
			cancel()
		}
		c.mu.Unlock()
		c.workers.Wait()
	}()
	closed := make(chan struct{})
	defer close(closed)

	go func() {
		select {
		case <-ctx.Done():
			_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "client disconnected"), time.Now().Add(time.Second))
			ws.Close()
		case <-closed:
		}
	}()
	if callbacks.idle != nil {
		callbacks.idle.start()
	}
	if callbacks.Connected != nil {
		callbacks.Connected()
	}
	started := time.Now()
	defer func() { result.online = time.Since(started) }()
	// Apply the negotiated heartbeat policy to this connection.
	heartbeat := resp.Header.Get(protocol.HeartbeatHeader) == "1"
	refreshDeadline := func() error {
		if heartbeat {
			return ws.SetReadDeadline(time.Now().Add(protocol.HeartbeatTimeout))
		}
		return nil
	}
	defaultPing := ws.PingHandler()
	ws.SetPingHandler(func(payload string) error {
		if err := refreshDeadline(); err != nil {
			return err
		}
		return defaultPing(payload)
	})
	_ = refreshDeadline()
	ws.SetReadLimit(2 << 20)
	for {
		var m protocol.Message
		if err := ws.ReadJSON(&m); err != nil {
			if ctx.Err() != nil {
				return attemptResult{}
			}
			return attemptResult{err: i18n.NewError("disconnected.w", err), permanent: websocket.IsCloseError(err, websocket.ClosePolicyViolation, websocket.CloseProtocolError, websocket.CloseUnsupportedData)}
		}
		_ = refreshDeadline()
		switch m.Type {
		case "latency":
			if callbacks.Latency != nil && m.LatencyMS >= 0 && m.LatencyMS <= protocol.HeartbeatTimeout.Milliseconds() {
				callbacks.Latency(time.Duration(m.LatencyMS) * time.Millisecond)
			}
		case "api_call":
			if callbacks.idle != nil {
				var request APIRequest
				if json.Unmarshal(m.Request, &request) == nil && (request.Method == "execute" || request.Method == "control") {
					callbacks.idle.touch()
				}
			}
			c.startAPICall(m)
		case "api_cancel":
			c.mu.Lock()
			if cancel := c.readers[m.ID]; cancel != nil {
				cancel()
			}
			c.mu.Unlock()
		case "p2p_start":
			if callbacks.idle != nil {
				callbacks.idle.touch()
			}
			if m.P2P != nil && len(m.P2P.Script) <= 64<<10 && len(m.P2P.StopScript) <= 64<<10 && len(m.P2P.ConfigBase64) <= 32<<10 {
				c.startBootstrap(m)
			}
		case "p2p_stop":
			c.mu.Lock()
			if cancel := c.bootstraps[m.ID]; cancel != nil {
				cancel()
			}
			c.mu.Unlock()
		}

	}
}
