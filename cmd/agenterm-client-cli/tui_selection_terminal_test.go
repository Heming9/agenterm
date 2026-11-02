package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

type selectionTestOutput struct {
	buffer   bytes.Buffer
	mu       sync.Mutex
	selected chan struct{}
	once     sync.Once
}

func (w *selectionTestOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buffer.Write(p)
	if strings.Contains(w.buffer.String(), "48;2;") {
		w.once.Do(func() { close(w.selected) })
	}
	return n, err
}

func (w *selectionTestOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

// Exercise Bubble Tea's actual SGR decoder and renderer, rather than constructing
// MouseMsg values directly. The fake clipboard keeps the user's clipboard intact.
func TestTUISelectionTerminalMouseAndClipboard(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(profile)
	m := selectionTUI(t, 80, 12)
	m.appendLog("output", "A中文👩‍💻Z\nhidden\n")
	copied := make(chan string, 1)
	m.clipboard.write = func(_ context.Context, text string) (string, error) {
		copied <- text
		return "tui.selection.copied", nil
	}
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	output := selectionTestOutput{selected: make(chan struct{})}
	program := tea.NewProgram(m, tea.WithInput(input), tea.WithOutput(&output), tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := program.Run(); done <- err }()
	t.Cleanup(func() { program.Kill(); program.Wait() })
	// Click the preview, then drag across partial wide cells in the expanded
	// line. SGR coordinates are one-based; the log starts on terminal row 3.
	_, err := io.WriteString(writer, "\x1b[<0;1;3M\x1b[<0;1;3m\x1b[<0;5;3M\x1b[<32;9;3M\x1b[<0;9;3m")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-copied:
		if text != "中文👩‍💻" {
			t.Fatalf("terminal mouse input copied %q", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("terminal mouse release did not trigger copying")
	}
	select {
	case <-output.selected:
	case <-time.After(3 * time.Second):
		program.Quit()
		<-done
		t.Fatalf("renderer did not draw the selection background: %q", output.String())
	}
	program.Quit()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "48;2;") {
		t.Fatal("renderer did not draw the selection background")
	}
	if !strings.Contains(output.String(), "\x1b[?1002h") {
		t.Fatal("terminal mouse reporting was not enabled")
	}
}
