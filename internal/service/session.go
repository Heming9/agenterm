package service

import (
	"agenterm/internal/i18n"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"agenterm/internal/protocol"
)

const maxFinishedSessionsPerClient = 10
const defaultSessionReadBytes = 16 << 10
const maxSessionReadBytes = 64 << 10

type session struct {
	log            *slog.Logger
	id             string
	owner          string
	client         *client
	mu             sync.Mutex
	done           bool
	exitCode       int
	err            string
	timedOut       bool
	canceled       bool
	ended          time.Time
	changed        chan struct{}
	keepAlive      bool
	promoted       bool
	commandDone    bool
	commandID      string
	commandLog     *slog.Logger
	commandStarted time.Time
	stopRequested  bool
	command        string
	started        time.Time
}

func (p *session) signal() { close(p.changed); p.changed = make(chan struct{}) }
func (p *session) accept(m protocol.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return
	}
	switch m.Type {
	case "output":
		// Output is owned by the client; unsolicited bytes are never cached.
	case "session_retained":
		p.keepAlive, p.promoted = m.SessionPersistent, m.SessionPromoted
		p.signal()
	case "command_done":
		if m.RequestID != p.commandID || p.commandDone {
			return
		}
		p.logCommandCompletion(m)
		p.commandDone, p.exitCode, p.err = true, m.ExitCode, m.Error
		p.timedOut, p.canceled = m.TimedOut, m.Canceled
		p.signal()
	case "done":
		if !p.commandDone {
			p.logCommandCompletion(m)
		}
		p.commandDone = true
		p.done = true
		p.exitCode = m.ExitCode
		p.err = m.Error
		p.timedOut, p.canceled = m.TimedOut, m.Canceled
		p.ended = time.Now().UTC()
		if p.log != nil {
			level := slog.LevelInfo
			if m.ExitCode != 0 || m.Error != "" || m.TimedOut || m.Canceled {
				level = slog.LevelWarn
			}
			p.log.Log(context.Background(), level, "session completed", "duration_ms", time.Since(p.started).Milliseconds(),
				"exit_code", m.ExitCode, "timed_out", m.TimedOut, "canceled", m.Canceled, "has_error", m.Error != "")
		}
		p.signal()
	}
}

func (p *session) logCommandCompletion(m protocol.Message) {
	if p.commandLog == nil {
		return
	}
	level := slog.LevelInfo
	if m.ExitCode != 0 || m.Error != "" || m.TimedOut || m.Canceled {
		level = slog.LevelWarn
	}
	p.commandLog.Log(context.Background(), level, "command completed", "duration_ms", time.Since(p.commandStarted).Milliseconds(), "exit_code", m.ExitCode, "timed_out", m.TimedOut, "canceled", m.Canceled, "has_error", m.Error != "")
}

// Anonymous cleanup and pending stop remain busy; idle must permit reuse.
// Closed means unavailable, including a lost connection reported via error.
func (p *session) readStatusLocked() string {
	if p.done {
		return "closed"
	}
	if p.commandDone && p.keepAlive && !p.stopRequested {
		return "idle"
	}
	return "running"
}

func (p *session) readCommand(ctx context.Context, cursor int64, wait time.Duration, maxBytes int, commandID string) (map[string]any, error) {
	if maxBytes < 4 || maxBytes > maxSessionReadBytes {
		return nil, i18n.NewError("session.read.invalid.limit")
	}
	if cursor < 0 || wait < 0 || wait > 20*time.Second {
		return nil, i18n.NewError("session.read.invalid.arguments")
	}
	return p.readFromClient(ctx, "session_read", protocol.SessionReadRequest{Cursor: cursor, WaitMS: int(wait / time.Millisecond), MaxBytes: maxBytes, CommandID: commandID})
}

// Only the in-flight response passes through the server. No output history is
// kept here, and a disconnected owner cannot supply a result.
func (p *session) readFromClient(ctx context.Context, kind string, read protocol.SessionReadRequest) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c := p.client
	id := random()
	replies := make(chan protocol.Message, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, i18n.NewError("client.disconnected")
	}
	c.pending[id] = replies
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.send(protocol.Message{Type: kind, ID: id, SessionID: p.id, Read: &read}); err != nil {
		return nil, err
	}
	select {
	case reply, ok := <-replies:
		if !ok {
			return nil, i18n.NewError("client.disconnected")
		}
		if reply.Type != "session_result" {
			return nil, i18n.NewError("session.control.invalid.response")
		}
		if reply.Error != "" {
			return nil, fmt.Errorf("%s", reply.Error)
		}
		if reply.Result == nil {
			return nil, i18n.NewError("session.control.invalid.response")
		}
		return reply.Result, nil
	case <-ctx.Done():
		_ = c.send(protocol.Message{Type: "session_read_cancel", ID: id})
		return nil, ctx.Err()
	}
}

// Caller-supplied IDs are isolated by credential without changing tool fields.
func sessionKey(secret, id string) string { return secret + "\x00" + id }

func (s *runtimeState) getSession(secret, id string) (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.sessions[sessionKey(secret, id)]
	if p == nil || p.owner != secret {
		return nil, i18n.NewError("unknown.session")
	}
	return p, nil
}

// Listing also includes disconnected devices' retained results and does not
// require a currently connected target device. The token is the access boundary.
func (s *runtimeState) listSessions(secret, clientID string) []sessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]sessionInfo, 0)
	startedAt := make(map[string]time.Time)
	for id, p := range s.sessions {
		if p.owner != secret {
			continue
		}
		p.mu.Lock()
		if p.done && time.Since(p.ended) > 30*time.Minute {
			delete(s.sessions, id)
			p.mu.Unlock()
			continue
		}
		if clientID == "" || clientID == p.client.info.ID {
			info := sessionInfo{SessionID: p.id, ClientID: p.client.info.ID, Command: p.command, CommandID: p.commandID, Status: p.readStatusLocked()}
			startedAt[p.id] = p.started
			if p.commandDone || p.done {
				info.ExitCode, info.Error = new(p.exitCode), p.err
			}

			items = append(items, info)
		}
		p.mu.Unlock()
	}
	sort.Slice(items, func(i, j int) bool {
		if startedAt[items[i].SessionID].Equal(startedAt[items[j].SessionID]) {
			return items[i].SessionID < items[j].SessionID
		}
		return startedAt[items[i].SessionID].Before(startedAt[items[j].SessionID])
	})
	return items
}

func (s *runtimeState) controlSession(ctx context.Context, in toolRequest[sessionActionArgs]) (any, error) {
	if err := in.Args.validate(); err != nil {
		return nil, err
	}
	p, err := s.getSession(in.Secret, in.Args.SessionID)
	if err != nil {
		return nil, err
	}
	a := in.Args
	if a.Action == "stop" {
		wait := 1000
		if a.WaitMS != nil {
			wait = *a.WaitMS
		}
		return p.stop(ctx, time.Duration(wait)*time.Millisecond)
	}
	input := ""
	if a.Input != nil {
		input = *a.Input
	}
	return p.requestControl(ctx, a.Action, input)
}

// Controls wait for acknowledgment of the OS operation. Old clients without
// advertised support are rejected rather than silently ignoring new messages.
func (p *session) requestControl(ctx context.Context, action, input string) (sessionControlResult, error) {
	if err := ctx.Err(); err != nil {
		return sessionControlResult{}, err
	}
	if action != "interrupt" && action != "eof" && action != "input" {
		return sessionControlResult{}, i18n.NewError("session.control.unsupported")
	}
	c := p.client
	if action != "input" && !slices.Contains(c.info.Environment.SessionControls, action) {
		return sessionControlResult{}, i18n.NewError("session.control.unsupported")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	id := random()
	ch := make(chan protocol.Message, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return sessionControlResult{}, i18n.NewError("client.disconnected")
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.send(protocol.Message{Type: action, ID: p.id, RequestID: id, Input: input}); err != nil {
		return sessionControlResult{}, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return sessionControlResult{}, i18n.NewError("client.disconnected")
		}
		if m.Type != "control_done" {
			return sessionControlResult{}, i18n.NewError("session.control.invalid.response")
		}
		if m.Error != "" {
			return sessionControlResult{}, fmt.Errorf("%s", m.Error)
		}
		return sessionControlResult{OK: true}, nil
	case <-ctx.Done():
		return sessionControlResult{}, i18n.NewError("session.control.unconfirmed", ctx.Err())
	}
}

// A successful write only confirms delivery of the request to the connection.
// Completion is confirmed exclusively by the session's terminal state.
func (p *session) stop(ctx context.Context, wait time.Duration) (sessionStopResult, error) {
	if err := ctx.Err(); err != nil {
		return sessionStopResult{}, err
	}
	p.mu.Lock()
	if !p.done && !p.stopRequested {
		if err := p.client.send(protocol.Message{Type: "cancel", ID: p.id}); err != nil {
			p.mu.Unlock()
			return sessionStopResult{}, err
		}
		p.stopRequested = true
	}
	p.mu.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		p.mu.Lock()
		out := sessionStopResult{Status: "running"}
		if p.done {
			out.Status = "closed"
			out.ExitCode = new(p.exitCode)
			out.Error = p.err
		}
		changed := p.changed
		p.mu.Unlock()
		if out.Status == "closed" || wait <= 0 {
			return out, nil
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-timer.C:
			wait = 0
		case <-changed:
		}
	}
}
