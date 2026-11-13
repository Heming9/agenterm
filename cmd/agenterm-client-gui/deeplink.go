package main

import (
	"sync"

	"agenterm/internal/client"

	"fyne.io/fyne/v2"
)

func parseDeepLink(raw string) (string, string, error) { return client.ParseDeepLink(raw, "agenterm") }

func openDeepLink(view *clientView, raw string) {
	address, mode, err := parseDeepLink(raw)
	if err != nil {
		view.showConnectionError(err.Error())
		return
	}
	if view.closed {
		return
	}
	if view.connecting || view.connected {
		view.window.Show()
		view.window.RequestFocus()
		view.showConnectionError(tr("deep.link.already.connected"))
		return
	}
	if mode == client.ConnectionModeTemporary {
		view.modeSelect.SetSelected(tr("connection.mode.short.temporary"))
	} else {
		view.modeSelect.SetSelected(tr("connection.mode.short.persistent"))
	}
	view.token.SetText(address)
	view.connect()
	view.window.Show()
	view.window.RequestFocus()
}

var deepLinkState struct {
	sync.Mutex
	receiver func(string)
	pending  []string
}

func setDeepLinkReceiver(receiver func(string)) {
	deepLinkState.Lock()
	deepLinkState.receiver = receiver
	pending := deepLinkState.pending
	deepLinkState.pending = nil
	deepLinkState.Unlock()
	for _, raw := range pending {
		raw := raw
		fyne.Do(func() { receiver(raw) })
	}
}

func receiveDeepLink(raw string) {
	deepLinkState.Lock()
	receiver := deepLinkState.receiver
	if receiver == nil {
		deepLinkState.pending = append(deepLinkState.pending, raw)
		deepLinkState.Unlock()
		return
	}
	deepLinkState.Unlock()
	fyne.Do(func() { receiver(raw) })
}
