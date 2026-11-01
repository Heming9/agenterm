package main

import (
	"agenterm/internal/i18n"
	"net/http"
	"net/netip"
	"strings"
)

type clientOwner struct {
	ip, token string
	order     uint64
	used      bool // a successful client connection, not merely an invitation redemption
}

// The caller holds s.mu for both helpers.
func (s *server) onlineForIP(ip string) int {
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
func (s *server) trimPending(ip string, max int) {
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
		for key, invitation := range s.invitations {
			if invitation.token == oldest.token {
				delete(s.invitations, key)
			}
		}
		delete(s.tokens, oldest.token)
		delete(s.clientOwners, oldestSecret)
		delete(s.multiDevice, oldestSecret)
	}
}

// The reverse proxy must overwrite X-Real-IP and direct access must be restricted.
func (s *server) requestIP(r *http.Request) (string, error) {
	values := r.Header.Values("X-Real-IP")
	if len(values) != 1 {
		return "", i18n.NewError("client.ip.invalid")
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(values[0]))
	if err != nil || ip.Zone() != "" {
		return "", i18n.NewError("client.ip.invalid")
	}
	return ip.Unmap().String(), nil
}
