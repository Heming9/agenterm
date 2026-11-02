package main

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type clientInfo struct {
	Status      string            `json:"status"`
	ID          string            `json:"client_id"`
	Environment clientEnvironment `json:"client_info"`
	ConnectedAt time.Time         `json:"connected_at"`
}

type clientEnvironment struct {
	Hostname      string `json:"hostname"`
	Platform      string `json:"platform"`
	Arch          string `json:"arch"`
	Shell         string `json:"shell"`
	DefaultCwd    string `json:"default_cwd"`
	ClientVersion string `json:"client_version"`
}

// Hostnames and paths are base64 encoded to support non-ASCII text.
// Metadata is display-only and never used to authenticate or route commands.
func metadataHeader(r *http.Request, name string) string {
	value := r.Header.Get(name)
	if name == protocol.HostnameHeader || name == protocol.ShellHeader || name == protocol.DefaultCwdHeader {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			return ""
		}
		value = string(decoded)
	}
	limit := 255
	if name == protocol.ShellHeader || name == protocol.DefaultCwdHeader {
		limit = 4096
	}
	if len(value) > limit || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") {
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
			info := c.info
			info.Status = "Connected"
			c.mu.Lock()
			latencyMS := c.latencyMS
			c.mu.Unlock()
			if latencyMS > 0 {
				info.Status += fmt.Sprintf(" · %dms", latencyMS)
			}
			clients = append(clients, info)
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
