package main

import (
	"agenterm/internal/i18n"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
)

var uiLanguage = "en"

func tr(key string) string { return i18n.Text(uiLanguage, key) }

func configureLanguage(a fyne.App, win fyne.Window) {
	choice := a.Preferences().StringWithFallback("language", "auto")
	uiLanguage = i18n.Current()
	if choice != "auto" {
		uiLanguage = i18n.Resolve(choice)
	}
	// The saved setting applies next time so an active connection is never interrupted.
	set := func(value string) { a.Preferences().SetString("language", value) }
	win.SetMainMenu(fyne.NewMainMenu(fyne.NewMenu(tr("language.menu"),
		fyne.NewMenuItem(tr("language.system"), func() { set("auto") }),
		fyne.NewMenuItem(tr("language.chinese"), func() { set("zh-CN") }),
		fyne.NewMenuItem(tr("language.english"), func() { set("en") }),
		fyne.NewMenuItem(tr("restart.to.apply.language.changes"), nil),
	)))
}

func showLocalizedError(err error, win fyne.Window) {
	d := dialog.NewInformation(tr("dialog.error"), i18n.Localize(err, uiLanguage), win)
	d.SetDismissText(tr("dialog.close"))
	d.Show()
}
