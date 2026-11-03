package client

import "agenterm/internal/protocol"

// Shared by every connection in this process, including CLI and GUI entrypoints.
// A slot is reserved before starting the shell and released only after it exits.
var terminalSlots = make(chan struct{}, protocol.MaxTerminalsPerClient)

func acquireTerminalSlot() bool {
	select {
	case terminalSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (p *process) releaseTerminalSlot() {
	if p.ownsTerminalSlot {
		p.terminalSlotOnce.Do(func() { <-terminalSlots })
	}
}
