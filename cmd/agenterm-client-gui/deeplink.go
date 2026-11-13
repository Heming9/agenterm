package main

import (
	"agenterm/internal/client"
	"fyne.io/fyne/v2"
)

func parseDeepLink(raw string) (string, error) { return client.ParseDeepLink(raw, "agenterm") }

func openDeepLink(view *clientView, raw string) {
	address, err := parseDeepLink(raw)
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
	view.modeSelect.SetSelected(tr("connection.mode.short.persistent"))
	view.token.SetText(address)
	view.connect()
	view.window.Show()
	view.window.RequestFocus()
}

var deepLinkReceiver func(string)

func receiveDeepLink(raw string) {
	if deepLinkReceiver != nil {
		fyne.Do(func() { deepLinkReceiver(raw) })
	}
}
