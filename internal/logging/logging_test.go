package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStructuredFieldsLevelAndRedaction(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, Config{Format: "json", Level: slog.LevelInfo}, "runtime", "staging")
	logger.Debug("hidden")
	logger.With("Authorization", "bearer-secret", slog.Group("env", "DATABASE", "nested-secret")).Info("command completed",
		"client_token", "token-secret", "command", "private-command", "output", "private-output", "client_id", "abc123")
	if strings.Contains(output.String(), "hidden") {
		t.Fatal("DEBUG escaped level filter")
	}
	for _, secret := range []string{"bearer-secret", "nested-secret", "token-secret", "private-command", "private-output"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("secret escaped redaction: %s", secret)
		}
	}
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["component"] != "runtime" || entry["instance"] != "staging" || entry["client_id"] != "abc123" || entry["pid"] == nil || entry["version"] == nil {
		t.Fatal(entry)
	}
}

func TestConfigAndRequestCorrelation(t *testing.T) {
	t.Setenv("AGENTERM_LOG_FORMAT", "json")
	t.Setenv("AGENTERM_LOG_LEVEL", "debug")
	c, err := FromEnv()
	if err != nil || c.Format != "json" || c.Level != slog.LevelDebug {
		t.Fatalf("config: %+v %v", c, err)
	}
	t.Setenv("AGENTERM_LOG_LEVEL", "secret-invalid-level")
	if _, err := FromEnv(); err == nil || strings.Contains(err.Error(), "secret-invalid-level") {
		t.Fatal("invalid config accepted or echoed its value")
	}
	first, second := NewRequestID(), NewRequestID()
	if !ValidRequestID(first) || first == second || ValidRequestID("unsafe\nvalue") {
		t.Fatal("invalid request ID")
	}
	ctx := WithRequestID(context.Background(), first)
	if RequestID(context.WithoutCancel(ctx)) != first {
		t.Fatal("correlation lost across detached context")
	}
}

func TestLogFilesAppendPrivatelyAndRejectSymlinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	for _, line := range []string{"first\n", "second\n"} {
		file, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = file.WriteString(line)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	content, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(content) != "first\nsecond\n" || info.Mode().Perm() != 0600 {
		t.Fatalf("log contents/permissions: %q %v", content, info.Mode())
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if file, err := Open(link); err == nil {
		file.Close()
		t.Fatal("accepted symlink log sink")
	}
}
