package service

import (
	"agenterm/internal/logging"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type lockedLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func captureLogs(t *testing.T) *lockedLogBuffer {
	t.Helper()
	var output lockedLogBuffer
	previous := slog.Default()
	previousOutput, previousFlags := log.Writer(), log.Flags()
	slog.SetDefault(logging.New(&output, logging.Config{Format: "json", Level: slog.LevelDebug}, "test", "default"))
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})
	return &output
}

func TestHTTPObservabilitySanitizesRoutesAndPropagatesRequestIDs(t *testing.T) {
	logs := captureLogs(t)
	var seen string
	backend := observeHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = logging.RequestID(r.Context())
		w.WriteHeader(http.StatusUnauthorized)
	}), true, func(r *http.Request) string { return publicRoute(r.URL.Path, "/agenterm") })
	ingress := observeHTTP(backend, false, func(r *http.Request) string { return publicRoute(r.URL.Path, "/agenterm") })
	r := httptest.NewRequest("GET", "/agenterm/c/private-install-code?token=private-query", nil)
	spoofed := strings.Repeat("a", 32)
	r.Header.Set(logging.RequestIDHeader, spoofed)
	r.Header.Set("Authorization", "Bearer private-token")
	w := httptest.NewRecorder()
	ingress.ServeHTTP(w, r)
	if !logging.ValidRequestID(seen) || seen == spoofed || w.Header().Get(logging.RequestIDHeader) != seen {
		t.Fatalf("request ID not generated/propagated: %s %v", seen, w.Header())
	}
	for _, value := range []string{"private-install-code", "private-query", "private-token", spoofed} {
		if strings.Contains(logs.String(), value) {
			t.Fatalf("request data escaped: %s", value)
		}
	}
	decoder := json.NewDecoder(strings.NewReader(logs.String()))
	matched := 0
	for {
		var entry map[string]any
		if err := decoder.Decode(&entry); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		// A previous test's connection may finish closing after capture begins.
		// Validate both HTTP layers using this request's correlation ID.
		if entry["request_id"] != seen {
			continue
		}
		matched++
		if entry["route"] != "/c/{code}" || entry["request_id"] != seen || entry["status"] != float64(401) || entry["level"] != "WARN" {
			t.Fatal(entry)
		}
	}
	if matched != 2 {
		t.Fatalf("got %d correlated HTTP logs, want 2", matched)
	}
}

func TestControlClientPropagatesCorrelation(t *testing.T) {
	id := logging.NewRequestID()
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get(logging.RequestIDHeader)
		setRuntimeIdentityHeaders(w.Header())
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	c := &runtimeClient{http: &http.Client{Transport: rewriteControlTransport{url: server.URL}}}
	_, err := controlCall[map[string]any](logging.WithRequestID(context.Background(), id), c, "config", struct{}{})
	if err != nil || seen != id {
		t.Fatalf("control correlation: %s %v", seen, err)
	}
}

type rewriteControlTransport struct{ url string }

func (t rewriteControlTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.URL.Host = strings.TrimPrefix(t.url, "http://")
	return http.DefaultTransport.RoundTrip(r)
}

func TestHTTPObservabilityPreservesWebSocketUpgrade(t *testing.T) {
	// Exercise real Hijacker support, not only an httptest.ResponseRecorder.
	handler := observeHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, w.Header())
		if err != nil {
			return
		}
		defer conn.Close()
		conn.WriteMessage(websocket.TextMessage, []byte("connected"))
	}), false, func(*http.Request) string { return "/client" })
	server := httptest.NewServer(handler)
	defer server.Close()
	conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, data, err := conn.ReadMessage()
	if err != nil || string(data) != "connected" || !logging.ValidRequestID(response.Header.Get(logging.RequestIDHeader)) {
		t.Fatalf("upgrade: %q %v", data, err)
	}
}

func TestMCPToolLogsFailuresWithoutArgumentsOrResults(t *testing.T) {
	logs := captureLogs(t)
	handler := observeMCP(func(ctx context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
		if !logging.ValidRequestID(logging.RequestID(ctx)) {
			t.Error("stdio tool has no request ID")
		}
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "private-result"}}}, nil
	})
	_, err := handler(context.Background(), "tools/call", &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "execute_command", Arguments: json.RawMessage(`{"command":"private-command","client_token":"private-token"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `"failed":true`) || !strings.Contains(logs.String(), `"tool":"execute_command"`) {
		t.Fatal(logs.String())
	}
	for _, value := range []string{"private-command", "private-token", "private-result"} {
		if strings.Contains(logs.String(), value) {
			t.Fatalf("MCP payload escaped: %s", value)
		}
	}
}

func TestHTTPPanicLogsStackWithoutPanicValueOrSuccess(t *testing.T) {
	logs := captureLogs(t)
	handler := observeHTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("private-panic") }), false,
		func(*http.Request) string { return "/mcp" })
	func() {
		defer func() {
			if recover() != http.ErrAbortHandler {
				t.Error("panic did not abort the HTTP connection")
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/agenterm/mcp", nil))
	}()
	if strings.Contains(logs.String(), "private-panic") || !strings.Contains(logs.String(), `"status":500`) || !strings.Contains(logs.String(), `"stack":`) {
		t.Fatal(logs.String())
	}
}
