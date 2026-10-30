package main

import (
	_ "embed"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
)

//go:embed assets/app-icon-agent.png
var appIcon []byte

func main() {
	a := app.NewWithID("io.agenterm.client")
	a.SetIcon(fyne.NewStaticResource("app-icon.png", appIcon))
	a.Settings().SetTheme(newMonochromeTheme(false))
	win := a.NewWindow("Agenterm")
	win.SetPadded(false)
	view := newClientView(win)
	win.SetContent(view.content)
	win.Resize(fyne.NewSize(700, 420))
	win.SetCloseIntercept(func() {
		view.close()
		win.Close()
	})
	view.startUpdates()
	win.ShowAndRun()
}
