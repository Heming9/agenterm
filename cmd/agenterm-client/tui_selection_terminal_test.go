package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"agenterm/internal/client"
	tea "github.com/charmbracelet/bubbletea"
)

type selectionTerminalOutput struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	changed chan struct{}
}

func (o *selectionTerminalOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	n, err := o.buffer.Write(p)
	o.mu.Unlock()
	select {
	case o.changed <- struct{}{}:
	default:
	}
	return n, err
}

func (o *selectionTerminalOutput) snapshot() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.String()
}

// Exercise the real renderer: a model-only test cannot prove that lines beyond
// the viewport were printed into native scrollback instead of being cropped.
func TestTUISelectionPrintsFullHistoryOutsideAlternateScreen(t *testing.T) {
	m := newTUI("")
	m.Update(tuiEvent{connected: true})
	m.Update(tuiEvent{command: &client.CommandEvent{ID: "one", Command: "echo many lines\necho more lines"}})
	var history strings.Builder
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&history, "history-row-%03d\n", i)
	}
	m.Update(tuiEvent{id: "one", output: []byte(history.String())})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	input, inputWriter := io.Pipe()
	output := &selectionTerminalOutput{changed: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithAltScreen(), tea.WithMouseCellMotion())
	finished := make(chan struct{})
	var runErr error
	go func() {
		_, runErr = p.Run()
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		m.cancel()
		inputWriter.Close()
		input.Close()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("terminal program did not stop")
		}
	})
	waitFor := func(start int, parts ...string) string {
		t.Helper()
		for {
			text := output.snapshot()[start:]
			found := true
			for _, part := range parts {
				found = found && strings.Contains(text, part)
			}
			if found {
				return text
			}
			select {
			case <-output.changed:
			case <-finished:
				t.Fatalf("program stopped before printing %q: %v\noutput: %q", parts, runErr, text)
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %q\noutput: %q", parts, text)
			}
		}
	}
	p.Send(tea.WindowSizeMsg{Width: 80, Height: 10})
	waitFor(0, "▸ history-row-000")
	start := len(output.snapshot())
	p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	native := waitFor(start, "\x1b[?1049l", "\x1b[?1002l", "history-row-079", tr("tui.selection.help"))
	afterExit := native[strings.Index(native, "\x1b[?1049l"):]
	for i := 0; i < 80; i++ {
		if !strings.Contains(afterExit, fmt.Sprintf("history-row-%03d", i)) {
			t.Fatalf("history row %d was cropped to the viewport", i)
		}
	}
	if !strings.Contains(afterExit, "echo more lines") || strings.Contains(afterExit, "▸") || strings.Contains(afterExit, "\x1b[?1049h") {
		t.Fatalf("selection snapshot was folded or returned to alternate screen: %q", afterExit)
	}
	// Incoming commands continue to be collected while native history is shown.
	p.Send(tuiEvent{command: &client.CommandEvent{ID: "two", Command: "echo buffered-command"}})
	start = len(output.snapshot())
	p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	waitFor(start, "\x1b[?1049h", "\x1b[?1002h", "echo buffered-command")
	p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	<-finished
	if runErr != nil {
		t.Fatal(runErr)
	}
}
