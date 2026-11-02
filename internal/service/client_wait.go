package service

import (
	"context"
	"time"

	"agenterm/internal/i18n"
)

type clientWaitState struct {
	changed chan struct{}
	refs    int
}

// The caller holds s.mu. A broadcast only wakes requests for this credential.
func (s *runtimeState) notifyClientWaitersLocked(secret string) {
	if state := s.clientWaiters[secret]; state != nil {
		close(state.changed)
		state.changed = make(chan struct{})
	}
}

// waitForClients returns as soon as any device is online. Snapshotting and
// subscribing share the connection registry lock so a connection cannot be lost
// between the empty check and the wait. No background task outlives the request.
func (s *runtimeState) waitForClients(ctx context.Context, secret string, wait time.Duration) ([]clientInfo, error) {
	deadline := time.Now().Add(wait)
	var state *clientWaitState
	defer func() {
		if state != nil {
			s.mu.Lock()
			state.refs--
			if state.refs == 0 {
				delete(s.clientWaiters, secret)
			}
			s.mu.Unlock()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		now := time.Now()
		// Observe expiry even before the periodic token cleanup runs.
		if owner := s.clientOwners[secret]; owner != nil && !owner.expires.IsZero() {
			if !now.Before(owner.expires) {
				s.cleanupTokensLocked(now)
				s.mu.Unlock()
				return nil, i18n.NewError("client.token.expired")
			} else if owner.expires.Before(deadline) {
				deadline = owner.expires
			}
		}
		if !s.validSecretLocked(secret) {
			s.mu.Unlock()
			return nil, i18n.NewError("valid.client.token.is.required")
		}
		clients := s.listClientsLocked(secret)
		if len(clients) > 0 || !now.Before(deadline) {
			s.mu.Unlock()
			return clients, nil
		}
		if state == nil {
			if s.clientWaiters == nil {
				s.clientWaiters = make(map[string]*clientWaitState)
			}
			state = s.clientWaiters[secret]
			if state == nil {
				state = &clientWaitState{changed: make(chan struct{})}
				s.clientWaiters[secret] = state
			}
			state.refs++
		}
		changed := state.changed
		s.mu.Unlock()
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}
