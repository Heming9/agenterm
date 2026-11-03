package client

import "testing"

func TestClosedTerminalCannotBePromoted(t *testing.T) {
	terminal := &persistentTerminal{closed: true}
	terminal.retain("finished")
	if terminal.persistent || terminal.promoted {
		t.Fatal("closed terminal promoted")
	}
}
