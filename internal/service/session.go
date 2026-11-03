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
	"unicode/utf8"

	"agenterm/internal/protocol"
)

const maxSessionOutput = 1 << 20
const maxFinishedSessionsPerClient = 10
const defaultSessionReadBytes = 16 << 10
const maxSessionReadBytes = 64 << 10

type session struct {
	log            *slog.Logger
	id             string
	owner          string
	client         *client
	mu             sync.Mutex
	output         []byte
	offset         int64
	done           bool
	exitCode       int
	err            string
	timedOut       bool
	canceled       bool
	ended          time.Time
	changed        chan struct{}
	keepAlive      bool
	terminalName   string
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
		p.output = append(p.output, []byte(m.Output)...)
		if len(p.output) > maxSessionOutput {
			n := len(p.output) - maxSessionOutput
			for n < len(p.output) && !utf8.RuneStart(p.output[n]) {
				n++
			}
			p.output = append([]byte(nil), p.output[n:]...)
			p.offset += int64(n)
		}
		p.signal()
	case "session_retained":
		p.keepAlive, p.terminalName, p.promoted = m.SessionPersistent, m.TerminalName, m.SessionPromoted
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

func (p *session) readCommand(ctx context.Context, cursor int64, wait time.Duration, maxBytes int, commandID string) (map[string]any, error) {
	if maxBytes < 4 || maxBytes > maxSessionReadBytes {
		return nil, i18n.NewError("session.read.invalid.limit")
	}
	if cursor < 0 || wait < 0 || wait > 20*time.Second {
		return nil, i18n.NewError("session.read.invalid.arguments")
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		p.mu.Lock()
		if commandID != "" && p.commandID != commandID {
			p.mu.Unlock()
			return nil, i18n.NewError("session.command.changed")
		}
		end := p.offset + int64(len(p.output))
		if cursor > end {
			p.mu.Unlock()
			return nil, i18n.NewError("cursor.is.beyond.available.output")
		}
		truncated := cursor < p.offset
		if truncated {
			cursor = p.offset
		}
		if cursor < end || p.done || p.commandDone || wait == 0 {
			if cursor < end && !utf8.RuneStart(p.output[cursor-p.offset]) {
				p.mu.Unlock()
				return nil, i18n.NewError("session.cursor.invalid.boundary")
			}
			next := min(end, cursor+int64(maxBytes))
			for next < end && !utf8.RuneStart(p.output[next-p.offset]) {
				next--
			}
			text := string(p.output[cursor-p.offset : next-p.offset])
			result := map[string]any{"session_id": p.id, "output": text, "next_cursor": next, "has_more": next < end, "truncated": truncated, "done": p.done}
			if p.done || p.commandDone {
				result["exit_code"] = p.exitCode
				result["timed_out"] = p.timedOut
				result["canceled"] = p.canceled
				if p.err != "" {
					result["error"] = p.err
				}
			}
			result["persistent"], result["promoted"], result["terminal_name"] = p.keepAlive, p.promoted, p.terminalName
			result["session_alive"], result["command_done"], result["command_id"] = !p.done && !p.stopRequested, p.commandDone, p.commandID
			result["status"] = "running"
			if p.commandDone {
				result["status"] = "completed"
			}
			p.mu.Unlock()
			return result, nil
		}
		changed := p.changed
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			wait = 0
		case <-changed:
		}
	}
}
func (s *runtimeState) getSession(secret, id string) (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.sessions[id]
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
			info := sessionInfo{SessionID: p.id, ClientID: p.client.info.ID, Command: p.command,
				StartedAt: p.started, Status: "running", Done: p.done,
				TerminalName: p.terminalName, Persistent: p.keepAlive, Promoted: p.promoted, CommandID: p.commandID, CommandDone: p.commandDone, SessionAlive: !p.done && !p.stopRequested,
				OutputStartCursor: p.offset, OutputEndCursor: p.offset + int64(len(p.output))}
			if p.commandDone {
				info.Status = "idle"
			}
			if p.stopRequested {
				info.Status = "stopping"
			}
			if p.done {
				info.Status, info.EndedAt, info.ExitCode, info.Error = "finished", new(p.ended), new(p.exitCode), p.err
			}
			items = append(items, info)
		}
		p.mu.Unlock()
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].StartedAt.Equal(items[j].StartedAt) {
			return items[i].SessionID < items[j].SessionID
		}
		return items[i].StartedAt.Before(items[j].StartedAt)
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
	out, err := p.requestControl(ctx, a.Action, input)
	if a.Action == "input" {
		return sessionWriteResult{Accepted: err == nil}, err
	}
	return out, err
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
		return sessionControlResult{Delivered: true}, nil
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
	alreadyDone := p.done
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
		out := sessionStopResult{SessionID: p.id, Accepted: !alreadyDone, AlreadyDone: alreadyDone, Done: p.done, Status: "stopping"}
		if p.done {
			out.Status = "finished"
			if p.canceled {
				out.Status = "stopped"
			}
			out.ExitCode, out.TimedOut, out.Canceled = new(p.exitCode), new(p.timedOut), new(p.canceled)
			out.Error = p.err
		}
		changed := p.changed
		p.mu.Unlock()
		if out.Done || wait <= 0 {
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
