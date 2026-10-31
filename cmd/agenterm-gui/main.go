package main

import (
	"context"
	_ "embed"
	"fmt"
	"net/url"
	"os"

	"agenterm/internal/invite"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/dialog"
)

//go:embed assets/app-icon-agent.png
var appIcon []byte

func main() {
	invitation := ""
	for i := 1; i+1 < len(os.Args); i++ {
		if os.Args[i] == "-invite" {
			invitation = os.Args[i+1]
			break
		}
	}
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
				go func() {
					token, err := invite.Redeem(context.Background(), invitation)
					fyne.Do(func() {
						if err != nil {
							showLocalizedError(err, win)
							return
						}
						view.token.SetText(token)
						view.connect()
					})
				}()
			}, win)
			confirmation.SetConfirmText(tr("dialog.allow"))
			confirmation.SetDismissText(tr("dialog.cancel"))
			confirmation.Show()
		}
	}
	win.ShowAndRun()
}
