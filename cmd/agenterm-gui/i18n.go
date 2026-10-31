package main

import (
	"agenterm/internal/i18n"
	"fyne.io/fyne/v2"
)

var uiLanguage = "en"

func tr(en, zh string) string { return i18n.Text(uiLanguage, en, zh) }

func configureLanguage(a fyne.App, win fyne.Window) {
	choice := a.Preferences().StringWithFallback("language", "auto")
	uiLanguage = i18n.Current()
	if choice != "auto" {
		uiLanguage = i18n.Resolve(choice)
	}
	// The saved setting applies next time so an active connection is never interrupted.
	set := func(value string) { a.Preferences().SetString("language", value) }
	win.SetMainMenu(fyne.NewMainMenu(fyne.NewMenu("Language / 语言",
		fyne.NewMenuItem("System / 跟随系统", func() { set("auto") }),
		fyne.NewMenuItem("简体中文", func() { set("zh-CN") }),
		fyne.NewMenuItem("English", func() { set("en") }),
		fyne.NewMenuItem(tr("Restart to apply language changes", "重启后应用语言设置"), nil),
	)))
}
