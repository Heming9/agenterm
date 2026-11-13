package main

import (
	"net/url"
	"reflect"
	"testing"

	"agenterm/internal/client"
	"fyne.io/fyne/v2/test"
)

func TestParseDeepLink(t *testing.T) {
	address := "https://example.com/agenterm/c/Ab1234"
	valid := "agenterm://connect?" + url.Values{"url": {address}}.Encode()
	if got, mode, err := parseDeepLink(valid); err != nil || got != address || mode != "" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, requested := range []string{"temporary", "persistent"} {
		for _, path := range []string{"", "/"} {
			value := "agenterm://connect" + path + "?" + url.Values{"url": {address}, "mode": {requested}}.Encode()
			got, mode, err := parseDeepLink(value)
			if err != nil || got != address || mode != requested {
				t.Fatalf("mode %s path %q: %s %s %v", requested, path, got, mode, err)
			}
		}
	}
	for _, bad := range []string{
		"https://connect?url=" + url.QueryEscape(address),
		"agenterm://other?url=" + url.QueryEscape(address),
		"agenterm://connect?url=" + url.QueryEscape(address) + "&mode=other",
		"agenterm://connect/path?url=" + url.QueryEscape(address),
		"agenterm://connect//?url=" + url.QueryEscape(address),
		"agenterm://connect?url=" + url.QueryEscape(address) + "&url=" + url.QueryEscape(address),
		"agenterm://connect?url=" + url.QueryEscape("example.com/c/Ab1234"),
		"agenterm://connect?url=" + url.QueryEscape("https://example.com/c/Ab1234?secret=x"),
		"agenterm://connect?url=" + url.QueryEscape("https://user@example.com/c/Ab1234"),
	} {
		if _, _, err := parseDeepLink(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestReceiveDeepLinkBeforeReceiverIsReady(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	t.Cleanup(func() {
		deepLinkState.Lock()
		deepLinkState.receiver = nil
		deepLinkState.pending = nil
		deepLinkState.Unlock()
	})

	receiveDeepLink("early")
	var received []string
	setDeepLinkReceiver(func(raw string) { received = append(received, raw) })
	receiveDeepLink("ready")
	if !reflect.DeepEqual(received, []string{"early", "ready"}) {
		t.Fatalf("received links %v", received)
	}
}

func TestOpenDeepLinkAppliesSelectedMode(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	for _, mode := range []string{client.ConnectionModeTemporary, client.ConnectionModePersistent} {
		win := app.NewWindow("Agenterm")
		view := newClientView(win)
		win.SetContent(view.content)
		raw := "agenterm://connect?" + url.Values{"url": {"http://127.0.0.1:1/c/abc123"}, "mode": {mode}}.Encode()
		openDeepLink(view, raw)
		if !view.connecting || view.temporary != (mode == client.ConnectionModeTemporary) {
			t.Errorf("mode=%s connecting=%v temporary=%v", mode, view.connecting, view.temporary)
		}
		view.close()
		win.Close()
	}
}
