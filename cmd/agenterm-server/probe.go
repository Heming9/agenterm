package main

import (
	"agenterm/internal/protocol"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// A probe uses the real client route and WebSocket upgrade, but never redeems
// the invitation, registers a client, wakes waiters, or accepts commands.
func (s *server) probeConnection(w http.ResponseWriter, r *http.Request) {
	value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	token, err := protocol.Decode(value)
	s.mu.Lock()
	entry, exists := s.invitations[token.Secret]
	source, sourceErr := protocol.Decode(entry.token)
	valid := err == nil && r.Header.Get("Authorization") == "Bearer "+value &&
		sourceErr == nil && token.URL == source.URL && exists && time.Now().Before(entry.expires)
	s.mu.Unlock()
	if status, err := s.authorize(r.RemoteAddr, valid); err != nil {
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "300")
		}
		http.Error(w, err.Error(), status)
		return
	}
	if !acceptClientVersion(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	conn, err := (&websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}).Upgrade(w, r, w.Header())
	if err != nil {
		return
	}
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "probe complete"), time.Now().Add(time.Second))
	conn.Close()
}
