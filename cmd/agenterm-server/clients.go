package main

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
	"crypto/rand"
	"encoding/base64"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type clientInfo struct {
	ID          string    `json:"client_id"`
	Hostname    string    `json:"hostname"`
	Platform    string    `json:"platform"`
	ConnectedAt time.Time `json:"connected_at"`
}

// Hostnames are base64 encoded on the wire to support non-ASCII device names.
// Metadata is display-only and never used to authenticate or route commands.
func metadataHeader(r *http.Request, name string) string {
	value := r.Header.Get(name)
	if name == protocol.HostnameHeader {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			return ""
		}
		value = string(decoded)
	}
	if len(value) > 255 || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

// The caller holds s.mu for allocation and all target lookups.
func (s *server) newClientIDLocked() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	for {
		var id [6]byte
		for i := range id {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				panic(err)
			}
			id[i] = alphabet[n.Int64()]
		}
		if s.clients[string(id[:])] == nil {
			return string(id[:])
		}
	}
}

func (s *server) hasClientLocked(secret string) bool {
	for _, c := range s.clients {
		if c.secret == secret {
			return true
		}
	}
	return false
}

func (s *server) targetClientLocked(secret, id string) (*client, error) {
	if id != "" {
		if c := s.clients[id]; c != nil && c.secret == secret {
			return c, nil
		}
		return nil, i18n.NewError("unknown.client.id")
	}
	if s.multiDevice[secret] {
		return nil, i18n.NewError("client.id.required")
	}
	for _, c := range s.clients {
		if c.secret == secret {
			return c, nil
		}
	}
	return nil, i18n.NewError("no.client.connected")
}

func (s *server) listClients(secret string) []clientInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listClientsLocked(secret)
}

func (s *server) listClientsLocked(secret string) []clientInfo {
	clients := make([]clientInfo, 0)
	for _, c := range s.clients {
		if c.secret == secret {
			clients = append(clients, c.info)
		}
	}
	sort.Slice(clients, func(i, j int) bool {
		if clients[i].ConnectedAt.Equal(clients[j].ConnectedAt) {
			return clients[i].ID < clients[j].ID
		}
		return clients[i].ConnectedAt.Before(clients[j].ConnectedAt)
	})
	return clients
}
