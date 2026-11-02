package service

import (
	"agenterm/internal/i18n"
	"agenterm/internal/logging"
	"agenterm/internal/protocol"
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"
)

const maxCommandOutput = 1 << 20

func (s *runtimeState) execute(ctx context.Context, secret, clientID, command string, options protocol.ExecOptions, executionID ...string) (result protocol.CommandResult, err error) {
	result = protocol.CommandResult{ExitCode: -1}
	var stdout, stderr strings.Builder
	defer func() { result.Stdout, result.Stderr = stdout.String(), stderr.String() }()
	s.mu.Lock()
	c, err := s.targetClientLocked(secret, clientID)
	s.mu.Unlock()
	if err != nil {
		return result, err
	}
	id := random()
	if len(executionID) > 0 {
		id = executionID[0]
	}
	logger := logging.FromContext(ctx).With("client_id", c.info.ID, "command_id", id)
	started := time.Now()
	logger.InfoContext(ctx, "command started", "timeout_ms", options.TimeoutMS)
	defer func() {
		level := slog.LevelInfo
		if err != nil || result.Failed() {
			level = slog.LevelWarn
		}
		logger.Log(ctx, level, "command completed", "duration_ms", time.Since(started).Milliseconds(),
			"exit_code", result.ExitCode, "timed_out", result.TimedOut, "canceled", result.Canceled,
			"truncated", result.Truncated, "failed", err != nil || result.Failed(), "error_kind", logging.ErrorKind(err))
	}()
	ch := make(chan protocol.Message, 256)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return result, i18n.NewError("client.disconnected")
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.send(protocol.Message{Type: "exec", ID: id, Command: command, ExecOptions: options}); err != nil {
		return result, err
	}
	remaining := maxCommandOutput
	for {
		select {
		case <-ctx.Done():
			_ = c.send(protocol.Message{Type: "cancel", ID: id})
			result.Canceled = true
			result.Error = ctx.Err().Error()
			return result, nil
		case m, ok := <-ch:
			if !ok {
				result.Error = serverText("client.disconnected")
				return result, nil
			}
			switch m.Type {
			case "output":
				text := m.Output
				if len(text) > remaining {
					text = text[:remaining]
					for !utf8.ValidString(text) {
						text = text[:len(text)-1]
					}
					result.Truncated = true
				}
				remaining -= len(text)
				if m.Stream == "stderr" {
					stderr.WriteString(text)
				} else {
					stdout.WriteString(text)
				}
			case "done":
				result.ExitCode, result.Error = m.ExitCode, m.Error
				result.TimedOut, result.Canceled = m.TimedOut, m.Canceled
				return result, nil
			}
		}
	}
}
