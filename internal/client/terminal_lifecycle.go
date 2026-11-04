package client

import (
	"agenterm/internal/i18n"
)

func (t *persistentTerminal) stopRetentionTimerLocked() {
	if t.retentionTimer != nil {
		t.retentionTimer.Stop()
		t.retentionTimer = nil
	}
}

// Completion and retention use the same lock: exactly one wins. Once retained,
// finishing a command leaves the shell alive until explicitly closed or exited.
func (t *persistentTerminal) retain() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.retainLocked()
}

func (t *persistentTerminal) retainLocked() {
	if t.closed || t.persistent {
		return
	}
	t.persistent = true
	t.stopRetentionTimerLocked()
	t.history.retain()
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
