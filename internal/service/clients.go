package service

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

type clientInfo struct {
	ID          string            `json:"client_id"`
	Environment clientEnvironment `json:"client_info"`
	ConnectedAt time.Time         `json:"connected_at"`
}

// closeClients validates the whole selection before closing any connection.
func (s *runtimeState) closeClients(ctx context.Context, secret string, ids []string) (closeClientResult, error) {
	s.mu.Lock()
	if !s.validSecretLocked(secret) {
		s.mu.Unlock()
		return closeClientResult{}, i18n.NewError("valid.client.token.is.required")
	}
	selected := make([]*client, 0)
	if ids == nil {
		for _, info := range s.listClientsLocked(secret) {
			selected = append(selected, s.clients[info.ID])
		}
		s.revokeTokensLocked(map[string]bool{secret: true})
	} else {
		seen := make(map[string]bool)
		if len(ids) == 0 {
			s.mu.Unlock()
			return closeClientResult{}, i18n.NewError("unknown.client.id")
		}
		for _, id := range ids {
			c := s.clients[id]
			if c == nil || c.secret != secret || seen[id] {
				s.mu.Unlock()
				return closeClientResult{}, i18n.NewError("unknown.client.id")
			}
			seen[id] = true
			selected = append(selected, c)
		}
	}
	for _, c := range selected {
		c.closing = true
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
	}
	s.mu.Unlock()
	// Policy violation is understood by existing clients as a permanent close,
	// so an explicitly closed device does not immediately reconnect.
	out := closeClientResult{ClientIDs: make([]string, 0, len(selected))}
	var closeErrors []error
	for _, c := range selected {
		if err := c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "closed by server"), time.Now().Add(time.Second)); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("client %s: close signal delivery failed: %w", c.info.ID, err))
		}
		_ = c.conn.Close()
		out.ClientIDs = append(out.ClientIDs, c.info.ID)
	}
	for _, c := range selected {
		select {
		case <-c.done:
		case <-ctx.Done():
			return closeClientResult{}, ctx.Err()
		}
	}
	if len(closeErrors) != 0 {
		return closeClientResult{}, errors.Join(closeErrors...)
	}
	return out, nil
}

type clientEnvironment struct {
	Hostname        string   `json:"hostname"`
	Platform        string   `json:"platform"`
	Arch            string   `json:"arch"`
	Shell           string   `json:"shell"`
	DefaultCwd      string   `json:"default_cwd"`
	ClientVersion   string   `json:"client_version"`
	SessionControls []string `json:"session_controls"`
	TerminalAPI     bool     `json:"terminal_api"`
	P2P             bool     `json:"p2p"`
}

func sessionControlsHeader(r *http.Request) []string {
	controls := make([]string, 0, 2)
	for _, value := range strings.Split(r.Header.Get(protocol.SessionControlsHeader), ",") {
		value = strings.TrimSpace(value)
		if value == "interrupt" || value == "eof" {
			if !slices.Contains(controls, value) {
				controls = append(controls, value)
			}
		}
	}
	return controls
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
func (s *runtimeState) newClientIDLocked() string {
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

func (s *runtimeState) hasClientLocked(secret string) bool {
	for _, c := range s.clients {
		if c.secret == secret {
			return true
		}
	}
	return false
}

func (s *runtimeState) targetClientLocked(secret, id string) (*client, error) {
	if id != "" {
		if c := s.clients[id]; c != nil && c.secret == secret && !c.closing {
			return c, nil
		}
		return nil, i18n.NewError("unknown.client.id")
	}
	if s.multiDevice[secret] {
		return nil, i18n.NewError("client.id.required")
	}
	for _, c := range s.clients {
		if c.secret == secret && !c.closing {
			return c, nil
		}
	}
	return nil, i18n.NewError("no.client.connected")
}

func (s *runtimeState) listClientsLocked(secret string) []clientInfo {
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
