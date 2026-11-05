package service

import (
	"context"
	"time"
)

const tokenOfflineTTL = 3 * time.Minute

func (s *runtimeState) cleanupLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.mu.Lock()
			s.cleanupTokensLocked(now)
			s.mu.Unlock()
		}
	}
}

// The caller holds s.mu. Each sweep is linear in the retained state.
// Credentials without a lifecycle record are retained for compatibility.
func (s *runtimeState) cleanupTokensLocked(now time.Time) {
	online := make(map[string]bool)
	for _, c := range s.clients {
		online[c.secret] = true
	}
	expired := make(map[string]bool)
	for key, entry := range s.invitations {
		if !now.Before(entry.expires) {
			s.deleteInvitation(key)
			secret := s.tokens[entry.token]
			if owner := s.clientOwners[secret]; owner != nil && !owner.used && !online[secret] {
				expired[secret] = true
			}
		}
	}
	for secret, owner := range s.clientOwners {
		if !online[secret] && !owner.expires.IsZero() && !now.Before(owner.expires) {
			expired[secret] = true
		}
	}
	s.deleteTokensLocked(expired)
}

// The caller holds s.mu. Revoke credentials and all associated retained data.
func (s *runtimeState) deleteTokensLocked(secrets map[string]bool) {
	if len(secrets) == 0 {
		return
	}
	s.revokeTokensLocked(secrets)
}

// Revoke before disconnecting clients to prevent a concurrent reconnect.
func (s *runtimeState) revokeTokensLocked(secrets map[string]bool) {
	for key, entry := range s.invitations {
		if secrets[s.tokens[entry.token]] {
			s.deleteInvitation(key)
		}
	}
	for token, secret := range s.tokens {
		if secrets[secret] {
			delete(s.tokens, token)
		}
	}
	for secret := range secrets {
		delete(s.clientOwners, secret)
		delete(s.multiDevice, secret)
		s.notifyClientWaitersLocked(secret)
	}
}
