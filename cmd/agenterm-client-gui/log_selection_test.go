package main

import (
	"strings"
	"testing"
	"time"

	"agenterm/internal/client"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

func TestColoredLogSelectionAcrossEntries(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(newMonochromeTheme(false))
	w := a.NewWindow("test")
	defer w.Close()
	w.SetPadded(false)
	v := newClientView(w)
	defer v.close()
	w.SetContent(v.content)
	w.Resize(fyne.NewSize(700, 420))
	v.buffer.command(client.CommandEvent{ID: "one", Command: "echo hello", StartedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)})
	v.buffer.output("one", []byte("hello\nworld\n"))
	v.buffer.command(client.CommandEvent{ID: "two", Command: "pwd", StartedAt: time.Date(2026, 1, 1, 12, 0, 1, 0, time.UTC)})
	v.buffer.output("two", []byte("/tmp\n"))
	v.flushLogs()
	if v.logColors.String() != v.logSelection.Text || v.logSelection.Text != v.logText {
		t.Fatalf("colored and selectable text differ: %q != %q", v.logColors.String(), v.logSelection.Text)
	}
	pos := a.Driver().AbsolutePositionForObject(v.logSelection)
	test.Drag(w.Canvas(), pos.Add(fyne.NewPos(80, 70)), 79, 69)
	selected := v.logSelection.SelectedText()
	if !strings.Contains(selected, "echo hello") || !strings.Contains(selected, "hello\nworld") {
		t.Fatalf("selection did not cross command and output: %q", selected)
	}
	focused := w.Canvas().Focused()
	if shortcut, ok := focused.(fyne.Shortcutable); ok {
		shortcut.TypedShortcut(&fyne.ShortcutCopy{Clipboard: a.Clipboard()})
	} else {
		t.Fatalf("selection not focused: %T", focused)
	}
	if a.Clipboard().Content() != selected {
		t.Fatalf("copy %q != selection %q", a.Clipboard().Content(), selected)
	}
	// New output should not clear a selection that the user is copying.
	v.buffer.output("two", []byte("extra output\n"))
	v.flushLogs()
	if got := v.logSelection.SelectedText(); got != selected {
		t.Fatalf("selection changed after new output: %q != %q", got, selected)
	}
}
