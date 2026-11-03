package client

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
)

func (t *persistentTerminal) stopRetentionTimerLocked() {
	if t.retentionTimer != nil {
		t.retentionTimer.Stop()
		t.retentionTimer = nil
	}
}
func (t *persistentTerminal) lifecycleLocked(m *protocol.Message) {
	m.SessionPersistent, m.SessionPromoted = t.persistent, t.promoted
}

// Completion and retention use the same lock: exactly one wins. Once retained,
// finishing a command leaves the shell alive until explicitly closed or exited.
func (t *persistentTerminal) retain(commandID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.retainLocked(commandID)
}

func (t *persistentTerminal) retainLocked(commandID string) {
	if t.closed || t.persistent {
		return
	}
	t.persistent, t.promoted = true, true
	t.stopRetentionTimerLocked()
	m := protocol.Message{Type: "session_retained", ID: t.id, RequestID: commandID}
	t.lifecycleLocked(&m)
	_ = t.c.send(m)
}
func (t *persistentTerminal) inputError() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return i18n.NewError("session.already.ended")
	}
	if t.active == nil && !t.reserved {
		return i18n.NewError("session.input.requires.command")
	}
	return nil
}
