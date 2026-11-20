package service

import "time"

type clientOwner struct {
	ip, token      string
	controlKeyHash [32]byte
	order          uint64
	used           bool      // a successful client connection, not merely an invitation redemption
	expires        time.Time // first-connection deadline, or expiry after the last device disconnects
}

// The caller holds s.mu. Admission counts reservations immediately, while
// invitation eviction uses only connections that actually completed the handshake.
func (s *runtimeState) slotsForIP(ip string) (online, occupied int) {
	for secret, count := range s.connecting {
		if owner := s.clientOwners[secret]; owner != nil && owner.ip == ip {
			occupied += count
		}
	}
	for _, c := range s.clients {
		if owner := s.clientOwners[c.secret]; owner == nil || owner.ip != ip {
			continue
		}
		online++
		// A single-device takeover shares the slot already counted above.
		if s.multiDevice[c.secret] || s.connecting[c.secret] == 0 {
			occupied++
		}
	}
	return online, occupied
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
			if s.connecting[secret] > 0 {
				continue
			}
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
