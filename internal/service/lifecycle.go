package service

import (
	"agenterm/internal/components"
	"agenterm/internal/logging"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sync/errgroup"
)

// frontend has no device credentials, connections or execution state. A fresh
// instance can serve the same runtime immediately after an upgrade.
type frontend struct {
	control          control
	clientURL        string
	p2pAssets        string
	p2pDownloads     fs.FS
	p2pDownloadsOnce sync.Once
	mcpOnce          sync.Once
	mcpHandler       http.Handler
	stopping         atomic.Bool
	shutdown         context.CancelFunc
}

const remoteAddrHeader = "X-Agenterm-Ingress-Remote-Addr"

// Never replace a live socket or a symlink. Recover a socket left by a crash
// only after checking that no process is listening on that same inode.
func listenUnix(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("socket paths must be absolute")
	}
	if err := ensureSocketDirectory(path); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		old, statErr := os.Lstat(path)
		if statErr != nil || old.Mode()&os.ModeSocket == 0 {
			return nil, err
		}
		conn, dialErr := net.DialTimeout("unix", path, time.Second)
		if dialErr == nil {
			conn.Close()
			return nil, fmt.Errorf("socket %s is already in use: %w", path, syscall.EADDRINUSE)
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, err
		}
		current, statErr := os.Lstat(path)
		if statErr != nil || !os.SameFile(old, current) {
			return nil, err
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		listener, err = net.Listen("unix", path)
		if err != nil {
			return nil, err
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

func serveUntilStopped(ctx context.Context, srv *http.Server, listener net.Listener, grace time.Duration, ready ...func() error) error {
	finished := make(chan error, 1)
	go func() { finished <- srv.Serve(listener) }()
	if len(ready) > 0 {
		if err := ready[0](); err != nil {
			_ = srv.Close()
			<-finished
			return err
		}
	}
	select {
	case err := <-finished:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// Do not derive HTTP request contexts from the signal context: existing
		// requests must finish while Shutdown stops accepting new ones.
		shutdown, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			_ = srv.Close()
			return fmt.Errorf("HTTP drain: %w", err)
		}
		return nil
	}
}

func pathPrefix(clientURL string) string {
	u, _ := url.Parse(clientURL)
	return strings.TrimSuffix(u.Path, "/client")
}

// Runtime owns the public ingress so no client socket traverses the replaceable
// business process. Other requests are forwarded without interpreting MCP.
func (s *runtimeState) ingress(serverSocket string) http.Handler {
	transport := unixTransport(serverSocket)
	// Each new request resolves the managed link again, so server can publish
	// a ready replacement and drain its predecessor without restarting runtime.
	transport.DisableKeepAlives = true
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme, r.Out.URL.Host = "http", "server"
			r.Out.Host = r.In.Host
			// ReverseProxy removes X-Forwarded-* before Rewrite. Preserve the
			// explicitly configured IP source so HTTP and WebSocket auth use
			// the same trusted proxy identity and quota bucket.
			if s.clientIPHeader != "" {
				r.Out.Header.Del(s.clientIPHeader)
				for _, value := range r.In.Header.Values(s.clientIPHeader) {
					r.Out.Header.Add(s.clientIPHeader, value)
				}
			}
			// Never trust a public caller's claim about its original peer.
			r.Out.Header.Set(remoteAddrHeader, r.In.RemoteAddr)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logging.FromContext(r.Context()).ErrorContext(r.Context(), "server proxy failed", "error_kind", logging.ErrorKind(err), "error", err)
			w.Header().Set("Retry-After", "1")
			http.Error(w, "agenterm-server unavailable", http.StatusServiceUnavailable)
		},
	}
	mux := http.NewServeMux()
	prefix := pathPrefix(s.clientURL)
	mux.HandleFunc(prefix+"/client", s.connect)
	mux.HandleFunc(prefix+"/client/", s.connect)
	mux.HandleFunc(prefix+"/invite/redeem", s.redeemInvitation)
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("/", proxy)
	return observeHTTP(mux, false, func(r *http.Request) string { return publicRoute(r.URL.Path, prefix) })
}

func runRuntime(ctx context.Context, s *runtimeState, public net.Listener, controlListener net.Listener, serverSocket string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, c := range s.clients {
			_ = c.conn.Close()
		}
	}()
	group, lifetime := errgroup.WithContext(ctx)
	go s.cleanupLoop(lifetime, time.Minute)
	group.Go(func() error {
		return serveUntilStopped(lifetime, &http.Server{Handler: s.ingress(serverSocket), ReadHeaderTimeout: 10 * time.Second}, public, 5*time.Second)
	})
	group.Go(func() error {
		return serveUntilStopped(lifetime, &http.Server{Handler: s.controlHandler(cancel), ReadHeaderTimeout: 10 * time.Second}, controlListener, 5*time.Second)
	})
	err := group.Wait()
	slog.Info("runtime stopped", "error_kind", logging.ErrorKind(err))
	return err
}

func (s *frontend) socketHandler(assets http.Handler) http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_agenterm/shutdown" {
			if r.Method != http.MethodPost || r.Header.Get(remoteAddrHeader) != "" || s.shutdown == nil {
				http.NotFound(w, r)
				return
			}
			s.stopping.Store(true)
			s.shutdown()
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if _, err := controlCall[runtimeConfig](ctx, s.control, "config", struct{}{}); err != nil {
				logging.FromContext(r.Context()).ErrorContext(r.Context(), "runtime health check failed", "error_kind", logging.ErrorKind(err))
				http.Error(w, "runtime unavailable", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		r.RemoteAddr = r.Header.Get(remoteAddrHeader)
		r.Header.Del(remoteAddrHeader)
		if _, _, err := net.SplitHostPort(r.RemoteAddr); err != nil {
			http.Error(w, "missing runtime ingress identity", http.StatusBadRequest)
			return
		}
		assets.ServeHTTP(w, r)
	})
	return observeHTTP(handler, true, func(r *http.Request) string { return publicRoute(r.URL.Path, pathPrefix(s.clientURL)) })
}

// runtimeWorkerMain is accessible only through the server's inherited startup pipe.
func runtimeWorkerMain() {
	output, ready := runtimeStartupOutput()
	flags := flag.NewFlagSet("runtime-worker", flag.ExitOnError)
	flags.SetOutput(output)
	options := addRuntimeLaunchFlags(flags)
	controlSocket := flags.String("runtime-socket", "", "Internal control socket")
	upstreamSocket := flags.String("server-socket", "", "Internal backend entrypoint")
	if err := flags.Parse(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
	options.Fields = make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { options.Fields[f.Name] = true })
	options.Settings.ServerSocket = *upstreamSocket
	closeLog, err := configureServiceLogging("runtime", os.Getenv("AGENTERM_LOG_INSTANCE"))
	if err != nil {
		fmt.Fprintln(output, err)
		os.Exit(1)
	}
	defer closeLog()
	if err := runtimeMain(options, *controlSocket, output, ready); err != nil {
		// Startup errors must reach the launcher even when the independent log
		// sink is a file. After readiness this pipe is already closed.
		fmt.Fprintln(output, err)
		slog.Error("runtime failed", "error_kind", logging.ErrorKind(err), "error", err)
		os.Exit(1)
	}
}

func runtimeMain(options *runtimeLaunchOptions, controlSocket string, output io.Writer, ready func()) error {
	if err := validateRuntimeLaunch(options); err != nil {
		return err
	}
	settings := options.Settings
	u, err := parsePublicURL(settings.PublicURL, settings.Prefix)
	if err != nil {
		return err
	}
	publicBase := u.String()
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path += "/client"
	password := options.Password
	if !settings.NoPassword && !options.Fields["passwd"] {
		password = randomManagementKey()
	}
	s := &runtimeState{adminKey: password, noPassword: settings.NoPassword, clientURL: u.String(), clientIPHeader: http.CanonicalHeaderKey(settings.ClientIPHeader), maxClientsPerIP: settings.MaxClientsPerIP}
	settings.PublicURL, settings.Prefix, settings.ClientIPHeader = publicBase, pathPrefix(s.clientURL), s.clientIPHeader
	s.startupSettings = &settings
	controlListener, err := listenUnix(controlSocket)
	if err != nil {
		return err
	}
	defer controlListener.Close()
	public, err := net.Listen("tcp", settings.Listen)
	if err != nil {
		return err
	}
	defer public.Close()
	if !settings.NoPassword && !options.Fields["passwd"] {
		fmt.Fprintf(output, serverText("mcp.management.key.save.securely.s"), password)
	}
	if settings.NoPassword {
		slog.Warn("management authentication disabled")
	}
	if !strings.EqualFold(os.Getenv("AGENTERM_LOG_FORMAT"), "json") {
		fmt.Fprintf(output, serverText("mcp.endpoint.s.mcp"), publicBase)
	}
	slog.Info("runtime ready", "listen", public.Addr().String(), "mcp_endpoint", publicBase+"/mcp")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ready()
	return runRuntime(ctx, s, public, controlListener, settings.ServerSocket)
}

func serverMain(paths instancePaths, stdio bool, expected runtimeConfig) error {
	c := newRuntimeClient(paths.Runtime)
	defer c.http.CloseIdleConnections()
	assets, err := components.Prepare(paths.Dir)
	if err != nil {
		return err
	}
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(signals)
	defer cancel()
	s := &frontend{control: c, clientURL: expected.ClientURL, p2pAssets: assets.P2PDirectory(), shutdown: cancel}
	candidate := paths.backendSocket()
	listener, err := listenUnix(candidate)
	if err != nil {
		return err
	}
	defer listener.Close()
	srv := &http.Server{Handler: s.socketHandler(s.routes(downloadTag, assets.ClientCLI())), ReadHeaderTimeout: 10 * time.Second}
	activate := func() error {
		ready, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		if err := waitBackend(ready, candidate); err != nil {
			return err
		}
		if err := publishBackend(ready, paths, candidate, expected.PID, c); err != nil {
			var drain *backendDrainError
			if !errors.As(err, &drain) {
				return err
			}
			slog.Warn("previous server drain failed", "error_kind", logging.ErrorKind(err))
		}
		slog.Info("server ready")
		return nil
	}
	go func() { <-ctx.Done(); s.stopping.Store(true) }()
	if stdio {
		return s.runStdio(ctx, srv, listener, &mcp.StdioTransport{}, activate)
	}
	err = serveUntilStopped(ctx, srv, listener, serverDrainTimeout, activate)
	slog.Info("server stopped", "error_kind", logging.ErrorKind(err))
	return err
}
