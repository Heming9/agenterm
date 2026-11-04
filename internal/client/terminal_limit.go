package client

// Shared by every connection in this process, including CLI and GUI entrypoints.
// A slot is reserved before starting the shell and released only after it exits.
var terminalSlots = make(chan struct{}, MaxTerminals)

func acquireTerminalSlot() bool {
	select {
	case terminalSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (p *process) releaseTerminalSlot() {
	p.terminalSlotOnce.Do(func() { <-terminalSlots })
}
