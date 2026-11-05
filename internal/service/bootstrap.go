package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"agenterm/internal/protocol"
)

// These immutable settings describe the running process. They contain no
// credentials and are never persisted. Explicit CLI changes require a restart.
type runtimeSettings struct {
	Listen          string
	PublicURL       string
	Prefix          string
	ServerSocket    string
	NoPassword      bool
	MaxClientsPerIP int
	ClientIPHeader  string
	EnableShortCode bool
}

type runtimeLaunchOptions struct {
	Settings runtimeSettings
	Password string
	Fields   map[string]bool
}

func addRuntimeLaunchFlags(flags *flag.FlagSet) *runtimeLaunchOptions {
	o := &runtimeLaunchOptions{}
	flags.StringVar(&o.Settings.Listen, "listen", "127.0.0.1:8080", "Public TCP address used when starting runtime")
	flags.StringVar(&o.Settings.PublicURL, "public-url", "http://127.0.0.1:8080", serverText("public.server.url.optional.agenterm.suffix"))
	flags.StringVar(&o.Settings.Prefix, "url-prefix", protocol.PathPrefix, serverText("server.url.prefix"))
	flags.StringVar(&o.Password, "passwd", "", serverText("mcp.management.key.generated.when.omitted"))
	flags.BoolVar(&o.Settings.NoPassword, "no-password", false, serverText("allow.mcp.management.access.without.a.password"))
	flags.IntVar(&o.Settings.MaxClientsPerIP, "max-clients-per-ip", 0, serverText("max.clients.per.ip"))
	flags.StringVar(&o.Settings.ClientIPHeader, "client-ip-header", "", serverText("client.ip.header"))
	flags.BoolVar(&o.Settings.EnableShortCode, "enable-short-code", false, serverText("enable.short.code"))
	return o
}

func ensureSocketDirectory(socket string) error {
	if !filepath.IsAbs(socket) {
		return errors.New("socket paths must be absolute")
	}
	dir := filepath.Dir(socket)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || !ownsSocketDirectory(info) {
		return fmt.Errorf("socket directory %s must be owned by the current user and private (mode 0700)", dir)
	}
	return nil
}

var runtimeSettingFlags = []string{"listen", "public-url", "url-prefix", "server-socket", "passwd", "no-password", "max-clients-per-ip", "client-ip-header", "enable-short-code"}

func (s *runtimeState) checkRuntimeSettings(_ context.Context, requested runtimeLaunchOptions) (bool, error) {
	active := s.startupSettings
	for _, name := range runtimeSettingFlags {
		if !requested.Fields[name] {
			continue
		}
		if active == nil {
			return false, errors.New("runtime cannot verify startup settings; update runtime before changing flags")
		}
		o := requested.Settings
		matches := false
		switch name {
		case "listen":
			matches = o.Listen == active.Listen
		case "public-url":
			prefix := active.Prefix
			if requested.Fields["url-prefix"] {
				prefix = o.Prefix
			}
			u, err := parsePublicURL(o.PublicURL, prefix)
			matches = err == nil && u.String() == active.PublicURL
		case "url-prefix":
			prefix, err := protocol.NormalizePathPrefix(o.Prefix)
			matches = err == nil && prefix == active.Prefix
		case "server-socket":
			matches = filepath.Clean(o.ServerSocket) == filepath.Clean(active.ServerSocket)
		case "passwd":
			matches = !s.noPassword && subtle.ConstantTimeCompare([]byte(requested.Password), []byte(s.adminKey)) == 1
		case "no-password":
			matches = o.NoPassword == active.NoPassword
		case "max-clients-per-ip":
			matches = o.MaxClientsPerIP == active.MaxClientsPerIP
		case "client-ip-header":
			matches = http.CanonicalHeaderKey(o.ClientIPHeader) == active.ClientIPHeader
		case "enable-short-code":
			matches = o.EnableShortCode == active.EnableShortCode
		}
		if !matches {
			if name == "server-socket" {
				return false, errors.New("instance uses different internal IPC paths; use the restart command to recreate it")
			}
			return false, &controlError{Status: http.StatusConflict, Message: fmt.Sprintf("running runtime has a different -%s setting; use the restart command to change it (existing connections and memory state will be lost)", name)}
		}
	}
	return true, nil
}

// A dedicated inherited pipe reports startup output, including a generated key,
// then closes. No stdout/stderr pipe remains attached to the launching process.
func runtimeStartupOutput() (io.Writer, func()) {
	if os.Getenv("AGENTERM_RUNTIME_BOOTSTRAP_FD") != "3" {
		return os.Stdout, func() {}
	}
	_ = os.Unsetenv("AGENTERM_RUNTIME_BOOTSTRAP_FD")
	pipe := os.NewFile(3, "runtime-startup")
	info, err := pipe.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		log.Fatal("runtime requires the server startup pipe")
	}
	return pipe, func() {
		_ = pipe.Close()
	}
}

func runtimeAbsent(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}

func ensureRuntime(ctx context.Context, socket string, options *runtimeLaunchOptions, output io.Writer) (runtimeConfig, error) {
	if err := validateRuntimeLaunch(options); err != nil {
		return runtimeConfig{}, err
	}
	if err := ensureSocketDirectory(socket); err != nil {
		return runtimeConfig{}, err
	}
	c := newRuntimeClient(socket)
	defer c.http.CloseIdleConnections()
	verify := func(config runtimeConfig) (runtimeConfig, error) {
		// Older runtimes may have short codes enabled and ignore this setting.
		// Require an explicit runtime report even when the flag is omitted.
		if config.ShortCodeEnabled == nil || *config.ShortCodeEnabled != options.Settings.EnableShortCode {
			return runtimeConfig{}, errors.New("running runtime has a different or unsupported short-code setting; use restart to apply the requested setting (existing connections and memory state will be lost)")
		}
		check := *options
		check.Fields = make(map[string]bool, len(options.Fields)+1)
		for name, set := range options.Fields {
			check.Fields[name] = set
		}
		check.Fields["server-socket"] = true // internal route, never a public flag
		_, err := controlCall[bool](ctx, c, "runtime/check", check)
		return config, err
	}
	config, err := controlCall[runtimeConfig](ctx, c, "config", struct{}{})
	if err == nil {
		return verify(config)
	}
	if !runtimeAbsent(err) {
		return runtimeConfig{}, err
	}
	if _, err := parsePublicURL(options.Settings.PublicURL, options.Settings.Prefix); err != nil {
		return runtimeConfig{}, err
	}
	if err := ensureSocketDirectory(options.Settings.ServerSocket); err != nil {
		return runtimeConfig{}, err
	}
	command, err := os.Executable()
	if err != nil {
		return runtimeConfig{}, err
	}
	args := []string{}
	o := options.Settings
	args = append(args, "-runtime-socket", socket, "-server-socket", o.ServerSocket,
		"-listen", o.Listen, "-public-url", o.PublicURL, "-url-prefix", o.Prefix,
		"-max-clients-per-ip", strconv.Itoa(o.MaxClientsPerIP), "-client-ip-header", o.ClientIPHeader)
	if options.Fields["passwd"] {
		args = append(args, "-passwd", options.Password)
	}
	if o.EnableShortCode {
		args = append(args, "-enable-short-code")
	}
	if o.NoPassword {
		args = append(args, "-no-password")
	}
	cmd := exec.Command(command, args...)
	if err := detachRuntime(cmd); err != nil {
		return runtimeConfig{}, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return runtimeConfig{}, err
	}
	defer reader.Close()
	defer writer.Close()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return runtimeConfig{}, err
	}
	defer null.Close()
	runtimeLog, closeRuntimeLog, err := runtimeLogOutput(socket)
	if err != nil {
		return runtimeConfig{}, err
	}
	if closeRuntimeLog {
		defer runtimeLog.Close()
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, runtimeLog
	cmd.ExtraFiles = []*os.File{writer}
	instance := filepath.Base(filepath.Dir(socket))
	if instance == "agenterm-"+strconv.Itoa(os.Getuid()) {
		instance = defaultInstance
	}
	cmd.Env = append(os.Environ(), "AGENTERM_RUNTIME_BOOTSTRAP_FD=3", "AGENTERM_INTERNAL_RUNTIME=1", "AGENTERM_LOG_INSTANCE="+instance)
	slog.Info("runtime starting", "runtime_log", runtimeLog.Name())
	if err := cmd.Start(); err != nil {
		return runtimeConfig{}, fmt.Errorf("start runtime: %w", err)
	}
	_ = writer.Close()
	drained := make(chan struct{})
	var startup strings.Builder
	go func() { _, _ = io.Copy(&startup, io.LimitReader(reader, 1<<20)); close(drained) }()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var exitErr error
	childExited := false
	for {
		probe, cancelProbe := context.WithTimeout(ctx, 300*time.Millisecond)
		config, err = controlCall[runtimeConfig](probe, c, "config", struct{}{})
		cancelProbe()
		if err == nil {
			// The winning runtime may belong to a concurrent launcher. Its API
			// settings, not the attempted child command, determine compatibility.
			select {
			case <-drained:
			case <-ctx.Done():
				return runtimeConfig{}, ctx.Err()
			}
			if config.PID == cmd.Process.Pid {
				_, _ = io.WriteString(output, startup.String())
			}
			return verify(config)
		}
		var apiError *controlError
		var incompatible *runtimeCompatibilityError
		if errors.As(err, &incompatible) {
			return runtimeConfig{}, err
		}
		if errors.As(err, &apiError) {
			_ = cmd.Process.Signal(os.Interrupt)
			return runtimeConfig{}, err
		}
		if childExited {
			// A second launcher may still be bringing up the live socket.
			conn, dialErr := net.DialTimeout("unix", socket, 100*time.Millisecond)
			if dialErr == nil {
				conn.Close()
			} else {
				select {
				case <-drained:
					_, _ = io.WriteString(output, startup.String())
				case <-ctx.Done():
				}
				return runtimeConfig{}, fmt.Errorf("runtime exited before ready: %v", exitErr)
			}
		}
		select {
		case exitErr = <-exited:
			childExited = true
		case <-ticker.C:
		case <-ctx.Done():
			if !childExited {
				_ = cmd.Process.Signal(os.Interrupt)
			}
			return runtimeConfig{}, fmt.Errorf("waiting for runtime: %w", ctx.Err())
		}
	}
}

// All lifecycle operations use private owner-only sockets. Public ingress
// requests cannot access the frontend shutdown endpoint.
func stopManagedServices(ctx context.Context, paths instancePaths) error {
	lock, err := paths.lock(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	runtimeSocket, serverSocket := paths.Runtime, paths.Server
	if err := ensureSocketDirectory(runtimeSocket); err != nil {
		return err
	}
	control := newRuntimeClient(runtimeSocket)
	defer control.http.CloseIdleConnections()
	previous, err := controlCall[runtimeConfig](ctx, control, "config", struct{}{})
	if err != nil && !runtimeAbsent(err) {
		return err
	}
	if err := stopBackend(ctx, serverSocket); err != nil {
		return err
	}
	if _, err := controlCall[bool](ctx, control, "runtime/shutdown", struct{}{}); err != nil && !runtimeAbsent(err) {
		return err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := controlCall[runtimeConfig](ctx, control, "config", struct{}{})
		if runtimeAbsent(err) && !runtimeProcessAlive(previous.PID) {
			if info, err := os.Lstat(paths.Server); err == nil && info.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(paths.Server)
			}
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func validateRuntimeLaunch(options *runtimeLaunchOptions) error {
	if options.Settings.MaxClientsPerIP < 0 {
		return errors.New(serverText("max.clients.per.ip.invalid"))
	}
	if options.Fields["passwd"] && (options.Password == "" || options.Settings.NoPassword) {
		return errors.New(serverText("passwd.must.be.nonempty.and.cannot.be"))
	}
	if !filepath.IsAbs(options.Settings.ServerSocket) {
		return errors.New("socket paths must be absolute")
	}
	return nil
}
