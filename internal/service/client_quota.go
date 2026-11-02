package service

import "time"

type clientOwner struct {
	ip, token string
	order     uint64
	used      bool      // a successful client connection, not merely an invitation redemption
	expires   time.Time // first-connection deadline, or expiry after the last device disconnects
}

// The caller holds s.mu for both helpers.
func (s *runtimeState) onlineForIP(ip string) int {
	n := 0
	for _, c := range s.clients {
		if owner := s.clientOwners[c.secret]; owner != nil && owner.ip == ip {
			n++
		}
	}
	return n
}

// Reclaim oldest credentials that have never connected, including redeemed
// invitations. Connected and previously connected credentials are retained.
func (s *runtimeState) trimPending(ip string, max int) {
	for {
		count := 0
		var oldestSecret string
		var oldest *clientOwner
		for secret, owner := range s.clientOwners {
			if owner.ip != ip || owner.used {
				continue
			}
			count++
			if oldest == nil || owner.order < oldest.order {
				oldest, oldestSecret = owner, secret
			}
		}
		if count <= max || oldest == nil {
			return
		}
		s.deleteTokensLocked(map[string]bool{oldestSecret: true})
	}
}
