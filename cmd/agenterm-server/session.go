package main

import (
	"agenterm/internal/i18n"
	"context"
	"sync"
	"time"

	"agenterm/internal/protocol"
)

const maxSessionOutput = 1 << 20
const maxSessionsPerToken = 6

type session struct {
	id       string
	owner    string
	client   *client
	mu       sync.Mutex
	output   []byte
	offset   int64
	done     bool
	exitCode int
	err      string
	timedOut bool
	canceled bool
	ended    time.Time
	changed  chan struct{}
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
			p.output = append([]byte(nil), p.output[n:]...)
			p.offset += int64(n)
		}
		p.signal()
	case "done":
		p.done = true
		p.exitCode = m.ExitCode
		p.err = m.Error
		p.timedOut, p.canceled = m.TimedOut, m.Canceled
		p.ended = time.Now()
		p.signal()
	}
}
func (p *session) read(ctx context.Context, cursor int64, wait time.Duration) (map[string]any, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		p.mu.Lock()
		end := p.offset + int64(len(p.output))
		if cursor > end {
			p.mu.Unlock()
			return nil, i18n.NewError("cursor.is.beyond.available.output")
		}
		truncated := cursor < p.offset
		if truncated {
			cursor = p.offset
		}
		if cursor < end || p.done || wait == 0 {
			text := string(p.output[cursor-p.offset:])
			result := map[string]any{"session_id": p.id, "output": text, "next_cursor": end, "truncated": truncated, "done": p.done}
			if p.done {
				result["exit_code"] = p.exitCode
				result["timed_out"] = p.timedOut
				result["canceled"] = p.canceled
				if p.err != "" {
					result["error"] = p.err
				}
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
func (s *server) startSession(secret, clientID, command string, options protocol.ExecOptions) (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.targetClientLocked(secret, clientID)
	if err != nil {
		return nil, err
	}
	if s.sessions == nil {
		s.sessions = make(map[string]*session)
	}
	owned := 0
	oldestFinishedID := ""
	var oldestFinishedAt time.Time
	for id, p := range s.sessions {
		p.mu.Lock()
		expired := p.done && time.Since(p.ended) > 30*time.Minute
		if !expired && p.owner == secret {
			owned++
			if p.done && (oldestFinishedID == "" || p.ended.Before(oldestFinishedAt)) {
				oldestFinishedID, oldestFinishedAt = id, p.ended
			}
		}
		p.mu.Unlock()
		if expired {
			delete(s.sessions, id)
		}
	}
	if owned >= maxSessionsPerToken && oldestFinishedID == "" {
		return nil, i18n.NewError("session.limit.reached.try.again.later")
	}
	p := &session{id: random(), owner: secret, client: c, changed: make(chan struct{})}
	ch := make(chan protocol.Message, 256)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, i18n.NewError("client.disconnected")
	}
	c.pending[p.id] = ch
	c.mu.Unlock()
	if err := c.send(protocol.Message{Type: "exec", ID: p.id, Command: command, ExecOptions: options, Interactive: true}); err != nil {
		c.mu.Lock()
		delete(c.pending, p.id)
		c.mu.Unlock()
		return nil, err
	}
	// Keep each token's history bounded without evicting running sessions or
	// allowing one token to consume another token's session allowance.
	if owned >= maxSessionsPerToken {
		delete(s.sessions, oldestFinishedID)
	}
	s.sessions[p.id] = p
	go func() {
		defer func() { c.mu.Lock(); delete(c.pending, p.id); c.mu.Unlock() }()
		for m := range ch {
			p.accept(m)
			if m.Type == "done" {
				return
			}
		}
		p.accept(protocol.Message{Type: "done", ExitCode: -1, Error: i18n.Text(i18n.Terminal(), "client.disconnected")})
	}()
	return p, nil
}
func (s *server) getSession(secret, id string) (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.sessions[id]
	if p == nil || p.owner != secret {
		return nil, i18n.NewError("unknown.session")
	}
	return p, nil
}
func (p *session) send(typ, input string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return i18n.NewError("session.already.ended")
	}
	return p.client.send(protocol.Message{Type: typ, ID: p.id, Input: input})
}
