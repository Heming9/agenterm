package service

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
	"context"
	"time"
)

// Execution ownership is separate from an HTTP waiter. Finished results remain
// bounded and readable until TTL expiry or eviction for a newer command.
type execution struct {
	owner  string
	cancel context.CancelFunc
	done   chan struct{}
	result protocol.CommandResult
	err    error
	ended  time.Time // guarded by runtimeState.mu
}

type executionRequest struct {
	Secret string
	ID     string
}

func (s *runtimeState) startExecution(ctx context.Context, in toolRequest[executeArgs]) (string, error) {
	if err := validateExecution(in.Args); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.targetClientLocked(in.Secret, in.Args.ClientID); err != nil {
		return "", err
	}
	if s.executions == nil {
		s.executions = make(map[string]*execution)
	}
	owned, oldest := 0, ""
	for id, p := range s.executions {
		if !p.ended.IsZero() && time.Since(p.ended) > 30*time.Minute {
			delete(s.executions, id)
			continue
		}
		if p.owner != in.Secret {
			continue
		}
		owned++
		if !p.ended.IsZero() && (oldest == "" || p.ended.Before(s.executions[oldest].ended)) {
			oldest = id
		}
	}
	if owned >= maxSessionsPerToken {
		if oldest == "" {
			return "", i18n.NewError("session.limit.reached.try.again.later")
		}
		delete(s.executions, oldest)
	}
	id := random()
	commandCtx, cancel := context.WithCancel(ctx)
	p := &execution{owner: in.Secret, cancel: cancel, done: make(chan struct{})}
	s.executions[id] = p
	go func() {
		defer cancel()
		p.result, p.err = s.execute(commandCtx, in.Secret, in.Args.ClientID, in.Args.Command, in.Args.ExecOptions, id)
		s.mu.Lock()
		p.ended = time.Now()
		close(p.done)
		s.mu.Unlock()
	}()
	return id, nil
}

func (s *runtimeState) getExecution(in executionRequest) (*execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.executions[in.ID]
	if p == nil || p.owner != in.Secret {
		return nil, i18n.NewError("unknown.session")
	}
	return p, nil
}

func (s *frontend) execute(ctx context.Context, secret string, args executeArgs) (protocol.CommandResult, error) {
	// Complete registration even if the caller cancels during the short start
	// RPC, so the live frontend can explicitly cancel the registered command.
	startCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	id, err := controlCall[string](startCtx, s.control, "execute/start", toolRequest[executeArgs]{Secret: secret, Args: args})
	stop()
	if err != nil {
		return protocol.CommandResult{}, err
	}
	in := executionRequest{Secret: secret, ID: id}
	out, err := controlCall[protocol.CommandResult](ctx, s.control, "execute/wait", in)
	if ctx.Err() != nil && !s.stopping.Load() {
		// A lost RPC alone never cancels execution. An explicit call from a
		// still-running frontend preserves MCP client cancellation semantics.
		cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = controlCall[bool](cancelCtx, s.control, "execute/cancel", in)
	}
	return out, err
}
