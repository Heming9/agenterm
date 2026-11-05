// Package logging configures slog and carries request correlation. Business code
// uses *slog.Logger directly; this package does not define another logging API.
package logging

import (
	"agenterm/internal/version"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const RequestIDHeader = "X-Request-ID"

type Config struct {
	Format string
	Level  slog.Level
}

func FromEnv() (Config, error) {
	c := Config{Format: strings.ToLower(os.Getenv("AGENTERM_LOG_FORMAT"))}
	if c.Format == "" {
		c.Format = "text"
	}
	if c.Format != "text" && c.Format != "json" {
		return c, errors.New("AGENTERM_LOG_FORMAT must be text or json")
	}
	if value := os.Getenv("AGENTERM_LOG_LEVEL"); value != "" {
		if err := c.Level.UnmarshalText([]byte(value)); err != nil {
			return c, errors.New("AGENTERM_LOG_LEVEL must be DEBUG, INFO, WARN or ERROR (optionally with a numeric offset)")
		}
	}
	return c, nil
}

func New(w io.Writer, c Config, component, instance string) *slog.Logger {
	options := &slog.HandlerOptions{Level: c.Level, ReplaceAttr: redact}
	var handler slog.Handler = slog.NewTextHandler(w, options)
	if c.Format == "json" {
		handler = slog.NewJSONHandler(w, options)
	}
	return slog.New(handler).With("component", component, "version", version.Current, "pid", os.Getpid(), "instance", instance)
}

// Redaction is a backstop for named fields, not a parser for arbitrary strings.
// Callers must keep secrets out of messages and opaque error strings as well.
func redact(groups []string, a slog.Attr) slog.Attr {
	if sensitive(a.Key) {
		a.Value = slog.StringValue("[REDACTED]")
		return a
	}
	for _, group := range groups {
		if sensitive(group) {
			a.Value = slog.StringValue("[REDACTED]")
			break
		}
	}
	return a
}

var keyNormalizer = strings.NewReplacer("_", "", "-", "", ".", "")

func sensitive(key string) bool {
	key = strings.ToLower(keyNormalizer.Replace(key))
	switch key {
	case "authorization", "password", "passwd", "adminkey", "managementkey", "secret", "token", "clienttoken", "controltoken", "controlkey", "invitation", "installcode", "command", "args", "cwd", "input", "output", "stdout", "stderr", "env", "headers", "body", "payload":
		return true
	}
	return false
}

// Open opens an append-only log owned by this user. Parent directories must
// already exist. Rotation and retention belong to the deployment platform.
func Open(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("log file path must be absolute")
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("log file must be a private regular file (mode 0600)")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return f, nil
}

type requestIDKey struct{}

func NewRequestID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic("cannot generate request ID")
	}
	return hex.EncodeToString(id[:])
}

func ValidRequestID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func FromContext(ctx context.Context) *slog.Logger {
	if id := RequestID(ctx); id != "" {
		return slog.Default().With("request_id", id)
	}
	return slog.Default()
}

// ErrorKind deliberately omits opaque messages, which may contain commands,
// terminal output, credentials, or peer-supplied WebSocket close reasons.
func ErrorKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, io.EOF):
		return "eof"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "timeout"
	}
	return fmt.Sprintf("%T", err)
}
