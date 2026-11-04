package service

import (
	terminal "agenterm/internal/client"
	"agenterm/internal/i18n"
	"agenterm/internal/logging"
	"agenterm/internal/protocol"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// The relay owns authentication, device selection and transport correlation.
// Session names, existence, state and result retention belong to the client API.
func (s *runtimeState) terminalClient(secret, id string) (*client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.validSecretLocked(secret) {
		return nil, i18n.NewError("valid.client.token.is.required")
	}
	c, err := s.targetClientLocked(secret, id)
	if err != nil {
		return nil, err
	}
	if !c.info.Environment.TerminalAPI {
		return nil, i18n.NewError("client.api.required")
	}
	return c, nil
}
func callClientAPI[T any](ctx context.Context, c *client, method string, args any) (out T, err error) {
	started := time.Now()
	defer func() {
		c.logger().InfoContext(ctx, "client API completed", "request_id", logging.RequestID(ctx), "method", method, "duration_ms", time.Since(started).Milliseconds(), "error_kind", logging.ErrorKind(err))
	}()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	params, err := json.Marshal(args)
	if err != nil {
		return out, err
	}
	request, err := json.Marshal(terminal.APIRequest{Method: method, Params: params})
	if err != nil {
		return out, err
	}
	id := random()
	replies := make(chan protocol.Message, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return out, i18n.NewError("client.disconnected")
	}
	c.pending[id] = replies
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.send(protocol.Message{Type: "api_call", ID: id, Request: request}); err != nil {
		return out, err
	}
	select {
	case reply, ok := <-replies:
		if !ok {
			return out, i18n.NewError("client.disconnected")
		}
		if reply.Type != "api_result" {
			return out, fmt.Errorf("invalid client API response")
		}
		if reply.Error != "" {
			return out, fmt.Errorf("%s", reply.Error)
		}
		if len(reply.Data) == 0 {
			return out, fmt.Errorf("missing client API result")
		}
		err = json.Unmarshal(reply.Data, &out)
		return out, err
	case <-ctx.Done():
		_ = c.send(protocol.Message{Type: "api_cancel", ID: id})
		return out, ctx.Err()
	}
}
func (s *runtimeState) executeCommand(ctx context.Context, in toolRequest[executeArgs]) (map[string]any, error) {
	c, err := s.terminalClient(in.Secret, in.Args.ClientID)
	if err != nil {
		return nil, err
	}
	a := in.Args
	return callClientAPI[map[string]any](ctx, c, "execute", a.ExecuteRequest)
}
func (s *runtimeState) readSession(ctx context.Context, in toolRequest[readSessionArgs]) (map[string]any, error) {
	c, err := s.terminalClient(in.Secret, in.Args.ClientID)
	if err != nil {
		return nil, err
	}
	a := in.Args
	return callClientAPI[map[string]any](ctx, c, "read", a.ReadRequest)
}
func (s *runtimeState) controlSession(ctx context.Context, in toolRequest[sessionActionArgs]) (map[string]any, error) {
	c, err := s.terminalClient(in.Secret, in.Args.ClientID)
	if err != nil {
		return nil, err
	}
	a := in.Args
	return callClientAPI[map[string]any](ctx, c, "control", a.ControlRequest)
}
func (s *runtimeState) listSessions(ctx context.Context, in toolRequest[listSessionsArgs]) (sessionListResult, error) {
	s.mu.Lock()
	if !s.validSecretLocked(in.Secret) {
		s.mu.Unlock()
		return sessionListResult{}, i18n.NewError("valid.client.token.is.required")
	}
	var clients []*client
	if in.Args.ClientID != "" {
		c, err := s.targetClientLocked(in.Secret, in.Args.ClientID)
		if err != nil {
			s.mu.Unlock()
			return sessionListResult{}, err
		}
		clients = append(clients, c)
	} else {
		for _, info := range s.listClientsLocked(in.Secret) {
			clients = append(clients, s.clients[info.ID])
		}
	}
	s.mu.Unlock()
	// Device order is deterministic; each client determines its own session order.
	type deviceResult struct {
		sessions []terminal.SessionInfo
		err      error
	}
	results := make([]deviceResult, len(clients))
	var workers sync.WaitGroup
	for i, c := range clients {
		workers.Go(func() {
			if !c.info.Environment.TerminalAPI {
				results[i].err = i18n.NewError("client.api.required")
				return
			}
			out, err := callClientAPI[terminal.ListResult](ctx, c, "list", struct{}{})
			results[i] = deviceResult{out.Sessions, err}
		})
	}
	workers.Wait()
	out := sessionListResult{Sessions: make([]sessionInfo, 0)}
	for i, result := range results {
		if result.err != nil {
			return sessionListResult{}, result.err
		}
		for _, info := range result.sessions {
			out.Sessions = append(out.Sessions, sessionInfo{SessionInfo: info, ClientID: clients[i].info.ID})
		}
	}
	return out, nil
}
