package service

import (
	"agenterm/internal/logging"
	"bufio"
	"context"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func configureServiceLogging(component, instance string) (func(), error) {
	config, err := logging.FromEnv()
	if err != nil {
		return nil, err
	}
	writer := os.Stderr
	if component == "server" && os.Getenv("AGENTERM_LOG_FILE") != "" {
		writer, err = logging.Open(os.Getenv("AGENTERM_LOG_FILE"))
		if err != nil {
			return nil, err
		}
	}
	previous := slog.Default()
	previousOutput, previousFlags := log.Writer(), log.Flags()
	slog.SetDefault(logging.New(writer, config, component, instance))
	return func() {
		slog.SetDefault(previous)
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
		if writer != os.Stderr {
			_ = writer.Close()
		}
	}, nil
}

// The default Runtime sink must not retain a terminal or MCP parent's pipe.
// stderr is an explicit opt-in for platform collectors (Docker/journald).
func runtimeLogOutput(socket string) (*os.File, bool, error) {
	path := os.Getenv("AGENTERM_RUNTIME_LOG_FILE")
	if path == "stderr" {
		return os.Stderr, false, nil
	}
	if path == "" {
		path = filepath.Join(filepath.Dir(socket), "runtime.log")
	}
	f, err := logging.Open(path)
	return f, true, err
}

// Only known route templates are logged. In particular /c/<installation-code>,
// arbitrary paths, query strings and headers never enter an access log.
func publicRoute(path, prefix string) string {
	if path == "/healthz" || path == "/livez" {
		return path
	}
	suffix, ok := strings.CutPrefix(path, prefix+"/")
	if !ok {
		return "unmatched"
	}
	switch suffix {
	case "mcp", "client", "invite/redeem":
		return "/" + suffix
	}
	if strings.HasPrefix(suffix, "c/") {
		return "/c/{code}"
	}
	if strings.HasPrefix(suffix, "downloads/") {
		return "/downloads/{asset}"
	}
	return "unmatched"
}

func logHTTPMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
		return method
	}
	return "OTHER"
}

type logResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *logResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *logResponseWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *logResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}
func (w *logResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *logResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.status = http.StatusSwitchingProtocols
	}
	return conn, rw, err
}

func observeHTTP(next http.Handler, trusted bool, route func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(logging.RequestIDHeader)
		if !trusted || !logging.ValidRequestID(id) {
			id = logging.NewRequestID()
		}
		r = r.WithContext(logging.WithRequestID(r.Context(), id))
		r.Header.Set(logging.RequestIDHeader, id)
		w.Header().Set(logging.RequestIDHeader, id)
		response := &logResponseWriter{ResponseWriter: w}
		started := time.Now()
		name := route(r)
		defer func() {
			panicked := recover()
			level := slog.LevelInfo
			status := response.status
			if status == 0 {
				status = http.StatusOK
			}
			if status >= 500 {
				level = slog.LevelError
			} else if status >= 400 {
				level = slog.LevelWarn
			} else if name == "/healthz" || name == "/livez" || strings.HasPrefix(name, "/v1/") {
				level = slog.LevelDebug
			}
			if panicked != nil {
				if panicked != http.ErrAbortHandler {
					// Panic values can contain credentials. Preserve the stack,
					// then abort as net/http normally would, without its raw dump.
					logging.FromContext(r.Context()).ErrorContext(r.Context(), "http handler panicked", "route", name, "stack", string(debug.Stack()))
				}
				level = slog.LevelError
				if response.status == 0 {
					status = http.StatusInternalServerError
				}
			}
			logging.FromContext(r.Context()).Log(r.Context(), level, "http request completed", "route", name,
				"method", logHTTPMethod(r.Method), "status", status, "duration_ms", time.Since(started).Milliseconds(), "aborted", panicked != nil)
			if panicked != nil {
				panic(http.ErrAbortHandler)
			}
		}()
		next.ServeHTTP(response, r)
	})
}

func observeMCP(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (result mcp.Result, err error) {
		if method != "tools/call" {
			return next(ctx, method, request)
		}
		if logging.RequestID(ctx) == "" {
			ctx = logging.WithRequestID(ctx, logging.NewRequestID())
		}
		tool := "unknown"
		if call, ok := request.(*mcp.CallToolRequest); ok && call.Params != nil {
			switch call.Params.Name {
			case "create_client", "list_clients", "execute_command", "session_start", "session_read", "session_list", "session_control":
				tool = call.Params.Name
			}
		}
		started := time.Now()
		logger := logging.FromContext(ctx).With("tool", tool)
		logger.DebugContext(ctx, "mcp tool started")
		defer func() {
			failed := err != nil
			if call, ok := result.(*mcp.CallToolResult); ok {
				failed = failed || call.IsError
			}
			level := slog.LevelInfo
			if failed {
				level = slog.LevelWarn
			}
			logger.Log(ctx, level, "mcp tool completed", "duration_ms", time.Since(started).Milliseconds(), "failed", failed, "error_kind", logging.ErrorKind(err))
		}()
		return next(ctx, method, request)
	}
}
