package main

import (
	_ "embed"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"

	"agenterm/internal/client"
	"agenterm/internal/i18n"
	"agenterm/internal/invite"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/dialog"
)

//go:embed assets/app-icon-agent.png
var appIcon []byte

func main() {
	options, err := client.ParseOptions(flag.NewFlagSet(os.Args[0], flag.ExitOnError), os.Args[1:], false,
		func(key string) string { return i18n.Text(i18n.Current(), key) })
	if err != nil {
		log.Fatal(err)
	}
	invitation := options.Invitation
	a := app.NewWithID("io.agenterm.client")
	a.SetIcon(fyne.NewStaticResource("app-icon.png", appIcon))
	a.Settings().SetTheme(newMonochromeTheme(false))
	win := a.NewWindow("Agenterm")
	configureLanguage(a, win)
	win.SetPadded(false)
	view := newClientView(win)
	win.SetContent(view.content)
	win.Resize(fyne.NewSize(700, 420))
	win.SetCloseIntercept(func() {
		view.close()
		win.Close()
	})
	view.startUpdates()
	if options.Token != "" {
		view.token.SetText(options.Token)
		view.connect()
	}
	if invitation != "" {
		endpoint, _, err := invite.Endpoint(invitation)
		if err != nil {
			showLocalizedError(err, win)
		} else {
			u, _ := url.Parse(endpoint)
			confirmation := dialog.NewConfirm(tr("connection.authorization"), fmt.Sprintf(tr("allow.server.s.to.execute.commands.with"), u.Host), func(allowed bool) {
				if !allowed {
					return
				}
				view.token.SetText(invitation)
				view.connectValue(invitation, true)
			}, win)
			confirmation.SetConfirmText(tr("dialog.allow"))
			confirmation.SetDismissText(tr("dialog.cancel"))
			confirmation.Show()
		}
	}
	win.ShowAndRun()
}
