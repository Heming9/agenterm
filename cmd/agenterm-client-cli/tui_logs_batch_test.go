package main

import (
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"agenterm/internal/client"
	tea "github.com/charmbracelet/bubbletea"
)

type streamingTestModel struct{ *tuiModel }

func (m streamingTestModel) Init() tea.Cmd { return m.waitEvent() }

func TestTUILogFramesThroughTerminalProgram(t *testing.T) {
	m := selectionTUI(t, 80, 12)
	m.Update(tuiEvent{command: &client.CommandEvent{ID: "one", Command: "echo first"}})
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	output := &selectionTestOutput{selected: make(chan struct{})}
	program := tea.NewProgram(streamingTestModel{m}, tea.WithInput(input), tea.WithOutput(output), tea.WithAltScreen(), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := program.Run(); done <- err }()
	t.Cleanup(func() { program.Kill(); program.Wait() })
	waitForText := func(text string) {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				if strings.Contains(output.String(), text) {
					return
				}
			case <-timer.C:
				t.Fatalf("terminal never rendered %q: %q", text, output.String())
			}
		}
	}
	// Quiet output must flush on its timer, even without a following event.
	m.events <- tuiEvent{id: "one", output: []byte("quiet frame\n")}
	waitForText("quiet frame")
	// The event waiter must rearm after a frame, then after a lifecycle event.
	m.events <- tuiEvent{command: &client.CommandEvent{ID: "two", Command: "next command"}}
	waitForText("next command")
	m.events <- tuiEvent{id: "two", output: []byte("next frame\n")}
	waitForText("next frame")
	program.Quit()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTUILogBatchPreservesEventOrder(t *testing.T) {
	sequential, batched := selectionTUI(t, 26, 14), selectionTUI(t, 26, 14)
	started := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	events := []tuiEvent{
		{command: &client.CommandEvent{ID: "one", Command: "echo 中文\necho second", StartedAt: started}},
		{id: "one", output: []byte("first\n\x1b[3")},
		{id: "one", output: []byte("1m红\x1b[0m\n")},
		{command: &client.CommandEvent{ID: "two", Command: "pwd", StartedAt: started}},
		{id: "two", output: []byte("/home/中🙂\n")},
		{id: "one", output: []byte("\xe4\xb8")},
		{id: "one", output: []byte("\xad\ncontinued\n")},
		{command: &client.CommandEvent{ID: "one", Done: true, Error: "failed"}},
		{command: &client.CommandEvent{ID: "two", Done: true}},
	}
	for _, event := range events {
		sequential.Update(event)
	}
	batched.Update(tuiLogBatch(events))
	if sequential.logText() != batched.logText() || sequential.View() != batched.View() || sequential.completed != batched.completed || !reflect.DeepEqual(sequential.folds, batched.folds) || !reflect.DeepEqual(sequential.softWraps, batched.softWraps) {
		t.Fatal("batching changed output order, decoding, folds, or rendered rows")
	}
}

func TestTUIWaitEventFlushesAtLifecycleBoundary(t *testing.T) {
	m := selectionTUI(t, 40, 12)
	m.Update(tuiEvent{command: &client.CommandEvent{ID: "one", Command: "echo one"}})
	m.events <- tuiEvent{id: "one", output: []byte("one\n")}
	m.events <- tuiEvent{id: "one", output: []byte("two\n")}
	m.events <- tuiEvent{command: &client.CommandEvent{ID: "one", Done: true}}
	m.events <- tuiEvent{connecting: true}
	message := m.waitEvent()()
	batch, ok := message.(tuiLogBatch)
	if !ok || len(batch) != 3 || len(m.events) != 1 {
		t.Fatalf("lifecycle boundary lost or delayed an event: %#v", message)
	}
	m.Update(message)
	if m.completed != 1 || !strings.Contains(m.logText(), "one\ntwo\n") {
		t.Fatal("completion ran before its pending output")
	}
	if _, ok := m.waitEvent()().(tuiEvent); !ok {
		t.Fatal("a lifecycle event was delayed for a log frame")
	}
}

func TestTUIWaitEventFlushesQuietAndBoundedOutput(t *testing.T) {
	for _, tc := range []struct{ count, bytes, want int }{
		{count: 1, bytes: 1, want: 1},
		{count: 256, bytes: 1, want: 256},
		{count: 3, bytes: 32 << 10, want: 2},
	} {
		m := selectionTUI(t, 40, 12)
		for range tc.count {
			m.events <- tuiEvent{id: "one", output: []byte(strings.Repeat("x", tc.bytes))}
		}
		done := make(chan tea.Msg, 1)
		go func() { done <- m.waitEvent()() }()
		select {
		case message := <-done:
			batch, ok := message.(tuiLogBatch)
			if !ok || len(batch) != tc.want || len(m.events) != tc.count-tc.want {
				t.Fatalf("output was lost: %#v", message)
			}
		case <-time.After(time.Second):
			t.Fatal("quiet output never reached the screen")
		}
	}
}

func TestTUILogBatchKeepsSelectionFrozen(t *testing.T) {
	m := selectionTUI(t, 40, 12)
	m.Update(tuiEvent{command: &client.CommandEvent{ID: "one", Command: "echo one"}})
	m.Update(tuiEvent{id: "one", output: []byte("first\nsecond\n")})
	selectionMouse(m, 0, 2, tea.MouseActionPress)
	selectionMouse(m, 5, 2, tea.MouseActionMotion)
	before, selected := m.viewport.View(), m.selectedText()
	m.Update(tuiLogBatch{{id: "one", output: []byte("third\n")}, {id: "one", output: []byte("fourth\n")}})
	if !m.logDirty || m.viewport.View() != before || m.selectedText() != selected {
		t.Fatal("streaming output moved the active selection")
	}
	m.clearSelection()
	if m.logDirty || !strings.Contains(m.logText(), "third\nfourth\n") {
		t.Fatal("clearing the selection lost deferred output")
	}
}

func TestTUILogCacheMatchesFreshLayout(t *testing.T) {
	m := selectionTUI(t, 24, 12)
	check := func() {
		t.Helper()
		before := m.View()
		folds, soft := append([]tuiFold(nil), m.folds...), m.softWraps
		for i := range m.spans {
			m.spans[i].layout = nil
		}
		m.refreshLog()
		if before != m.View() || !reflect.DeepEqual(folds, m.folds) || !reflect.DeepEqual(soft, m.softWraps) {
			t.Fatal("cached layout differs from a complete rebuild")
		}
	}
	m.appendLog("system", "12:00:00 $ ")
	check()
	m.appendLog("command", "echo 中文👩‍💻\necho next\n")
	check()
	m.appendLog("output", "first line\nsecond line\n")
	check()
	m.toggleFold(m.folds[len(m.folds)-1].start)
	m.appendLog("output", "longer third line 中文👩‍💻\n")
	check()
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	check()
	m.legacyCells = true
	m.refreshLog()
	check()
	m.legacyCells = false
	m.appendLog("error", strings.Repeat("中", maxTUILog/3)+"\n")
	check()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m.appendLog("output", "fresh output\n")
	check()
}
