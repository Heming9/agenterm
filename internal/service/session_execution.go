package service

import (
	"agenterm/internal/i18n"
	"agenterm/internal/logging"
	"agenterm/internal/protocol"
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

const defaultExecutionWaitMS = 10_000

type sessionExecution struct {
	SessionID       string `json:"session_id"`
	CommandID       string `json:"command_id"`
	Cursor          int64  `json:"cursor"`
	SessionCreated  bool   `json:"session_created"`
	AwaitSettlement bool   `json:"await_settlement,omitempty"`
}
type waitExecutionArgs struct {
	sessionExecution
	WaitMS int `json:"wait_ms"`
}

// This is an in-flight RPC, not a terminal registry. Its binding is supplied by
// the client before any terminal output is dispatched.
type pendingSessionCommand struct {
	client  *client
	log     *slog.Logger
	session *session
	cursor  int64
	created bool
}

func (s *runtimeState) executeInSession(ctx context.Context, in toolRequest[executeArgs]) (sessionExecution, error) {
	a := in.Args
	if err := validateExecution(a); err != nil {
		return sessionExecution{}, err
	}
	s.mu.Lock()
	var c *client
	var err error
	targetID := a.SessionID
	if a.Setup != nil {
		targetID = a.Setup.SessionID
	}
	if p := s.sessions[sessionKey(in.Secret, targetID)]; targetID != "" && p != nil && s.clients[p.client.info.ID] == p.client {
		c = p.client
		if a.ClientID != "" && a.ClientID != c.info.ID {
			s.mu.Unlock()
			return sessionExecution{}, i18n.NewError("unknown.client.id")
		}
	} else {
		c, err = s.targetClientLocked(in.Secret, a.ClientID)
	}
	if err != nil {
		s.mu.Unlock()
		return sessionExecution{}, err
	}
	if !c.info.Environment.PersistentSessions {
		s.mu.Unlock()
		return sessionExecution{}, i18n.NewError("session.client.upgrade.required")
	}
	s.pruneSessionHistoryLocked(c)
	id := random()
	request := &pendingSessionCommand{client: c, log: logging.FromContext(ctx)}
	if s.sessionRequests == nil {
		s.sessionRequests = make(map[string]*pendingSessionCommand)
	}
	s.sessionRequests[id] = request
	replies := make(chan protocol.Message, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		delete(s.sessionRequests, id)
		s.mu.Unlock()
		return sessionExecution{}, i18n.NewError("client.disconnected")
	}
	c.pending[id] = replies
	c.mu.Unlock()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessionRequests, id)
		s.mu.Unlock()
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	wait := defaultExecutionWaitMS
	if a.WaitMS != nil {
		wait = *a.WaitMS
	}
	if err := c.send(protocol.Message{Type: "session_exec", ID: id, RequestID: id, SessionID: a.SessionID, WaitMS: wait, Command: a.Command, Setup: a.Setup}); err != nil {
		return sessionExecution{}, err
	}
	select {
	case reply, ok := <-replies:
		if !ok {
			return sessionExecution{}, i18n.NewError("client.disconnected")
		}
		if reply.Type == "command_rejected" {
			return sessionExecution{}, fmt.Errorf("%s", reply.Error)
		}
		if reply.Type != "command_accepted" || request.session == nil {
			return sessionExecution{}, i18n.NewError("session.control.invalid.response")
		}
		return sessionExecution{SessionID: reply.SessionID, CommandID: id, Cursor: request.cursor, SessionCreated: request.created, AwaitSettlement: !reply.SessionPersistent}, nil
	case <-ctx.Done():
		return sessionExecution{}, ctx.Err()
	}
}

// Called synchronously by the WebSocket reader before routing this admission.
// That ordering ensures output sent immediately after the client acknowledgment
// has a cache destination, even when its original MCP waiter has disappeared.
func (s *runtimeState) bindAcceptedSession(c *client, m protocol.Message) error {
	if m.SessionID == "" || m.RequestID != m.ID {
		return i18n.NewError("session.control.invalid.response")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	request := s.sessionRequests[m.ID]
	if request != nil && request.client != c {
		return i18n.NewError("unknown.session")
	}
	p := s.sessions[sessionKey(c.secret, m.SessionID)]
	if p != nil && (p.owner != c.secret || (p.client != c && s.clients[p.client.info.ID] == p.client)) {
		return i18n.NewError("unknown.session")
	}
	if p == nil || m.SessionCreate {
		p = &session{id: m.SessionID, owner: c.secret, client: c, changed: make(chan struct{}), started: time.Now().UTC()}
		p.log = c.logger().With("session_id", p.id)
		if request != nil {
			p.log = request.log.With("client_id", c.info.ID, "session_id", p.id)
		}
		if s.sessions == nil {
			s.sessions = make(map[string]*session)
		}
		s.sessions[sessionKey(c.secret, p.id)] = p
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// Mirror facts from client admission. No shell is created, chosen or kept
	// alive by this cache, and no cached lifecycle field gates admission.
	p.keepAlive, p.promoted = m.SessionPersistent, m.SessionPromoted
	p.commandDone, p.done, p.stopRequested = false, false, false
	p.commandID, p.command = m.RequestID, m.Command
	commandStart := p.offset + int64(len(p.output))
	p.exitCode, p.err, p.canceled, p.timedOut = 0, "", false, false
	p.commandStarted = time.Now()
	p.commandLog = p.log.With("command_id", m.RequestID)
	if request != nil {
		p.commandLog = request.log.With("client_id", c.info.ID, "session_id", p.id, "command_id", m.RequestID)
		request.session, request.cursor, request.created = p, commandStart, m.SessionCreate
	}
	p.commandLog.Info("command started")
	p.signal()
	return nil
}

// The WebSocket reader applies terminal events before reading the next frame.
// Admission must observe every earlier output byte when recording its cursor.
func (s *runtimeState) acceptSessionEvent(c *client, m protocol.Message) bool {
	switch m.Type {
	case "output", "command_done", "session_retained", "done":
	default:
		return false
	}
	s.mu.Lock()
	p := s.sessions[sessionKey(c.secret, m.ID)]
	s.mu.Unlock()
	if p == nil || p.client != c {
		return false
	}
	p.accept(m)
	return true
}

// Server-owned storage retention only. This never sends a cancellation and
// never evicts a live terminal merely to satisfy a cache size preference.
func (s *runtimeState) pruneSessionHistoryLocked(c *client) {
	var finished []struct {
		id    string
		ended time.Time
	}
	for id, p := range s.sessions {
		p.mu.Lock()
		if p.done && time.Since(p.ended) > 30*time.Minute {
			delete(s.sessions, id)
			p.mu.Unlock()
			continue
		}
		if p.done && p.client == c {
			finished = append(finished, struct {
				id    string
				ended time.Time
			}{id, p.ended})
		}
		p.mu.Unlock()
	}
	if len(finished) > maxFinishedSessionsPerClient {
		sort.Slice(finished, func(i, j int) bool { return finished[i].ended.Before(finished[j].ended) })
		for _, record := range finished[:len(finished)-maxFinishedSessionsPerClient] {
			delete(s.sessions, record.id)
		}
	}
}

func (p *session) waitExecution(ctx context.Context, a waitExecutionArgs) (map[string]any, error) {
	timer := time.NewTimer(time.Duration(a.WaitMS) * time.Millisecond)
	defer timer.Stop()
	for {
		p.mu.Lock()
		if p.commandID != a.CommandID {
			p.mu.Unlock()
			return nil, i18n.NewError("session.command.changed")
		}
		finished, changed := p.commandDone || p.done, p.changed
		if a.AwaitSettlement {
			finished = p.done || p.keepAlive
		}
		p.mu.Unlock()
		if finished || (!a.AwaitSettlement && a.WaitMS == 0) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			a.WaitMS = 0
			// Anonymous lifetime is decided and acknowledged by the client.
			// Its retention event or actual shell exit settles this waiter.
		case <-changed:
		}
	}
	// Pin the command identity through the snapshot so a concurrent new command
	// cannot relabel the output of this execution.
	out, err := p.readCommand(ctx, a.Cursor, 0, defaultSessionReadBytes, a.CommandID)
	if err == nil {
		out["session_created"] = a.SessionCreated
	}
	return out, err
}

func (s *frontend) executeSession(ctx context.Context, secret string, a executeArgs) (map[string]any, error) {
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	started, err := controlCall[sessionExecution](startCtx, s.control, "session/execute", toolRequest[executeArgs]{Secret: secret, Args: a})
	cancel()
	if err != nil {
		return nil, err
	}
	wait := defaultExecutionWaitMS
	if a.WaitMS != nil {
		wait = *a.WaitMS
	}
	return controlCall[map[string]any](ctx, s.control, "session/wait", toolRequest[waitExecutionArgs]{Secret: secret, Args: waitExecutionArgs{sessionExecution: started, WaitMS: wait}})
}
