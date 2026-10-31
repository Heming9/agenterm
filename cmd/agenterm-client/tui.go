package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"agenterm/internal/client"
	"agenterm/internal/protocol"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const maxTUILog = 64 * 1024

type logSpan struct{ kind, text string }

var logStyles = map[string]lipgloss.Style{
	"system":  lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#4b677a", Dark: "#99b1c3"}),
	"command": lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#257445", Dark: "#85c99b"}),
	"error":   lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#c53030", Dark: "#ff8a8a"}),
}

var statusStyles = map[string]lipgloss.Style{
	"connected":    lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#257445", Dark: "#85c99b"}),
	"connecting":   lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#986000", Dark: "#e6be6a"}),
	"disconnected": lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#737373", Dark: "#a3a3a3"}),
}

type tuiEvent struct {
	generation uint64
	command    *client.CommandEvent
	id         string
	output     []byte
	connected  bool
	err        error
	finished   bool
}
type tuiModel struct {
	input                         textinput.Model
	viewport                      viewport.Model
	events                        chan tuiEvent
	ctx                           context.Context
	cancel                        context.CancelFunc
	state, address, message, logs string
	completed                     int
	generation                    uint64
	decoders                      map[string]*displayDecoder
	shells                        map[string]int
	nextShell                     int
	spans                         []logSpan
	lastID                        string
	width, height                 int
	connectionProgress            bool
}

func newTUI(token string) *tuiModel {
	ctx, cancel := context.WithCancel(context.Background())
	input := textinput.New()
	input.Placeholder = "Paste connection token"
	input.CharLimit = 4096
	input.SetValue(token)
	input.Focus()
	return &tuiModel{input: input, viewport: viewport.New(80, 15), events: make(chan tuiEvent, 256), ctx: ctx, cancel: cancel, state: "disconnected", decoders: make(map[string]*displayDecoder), shells: make(map[string]int)}
}
func runTUI(token string, connectionProgress bool) error {
	m := newTUI(token)
	m.connectionProgress = connectionProgress
	defer m.cancel()
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
func (m *tuiModel) Init() tea.Cmd {
	if m.input.Value() != "" {
		return tea.Batch(textinput.Blink, m.connect())
	}
	return textinput.Blink
}
func (m *tuiModel) send(generation uint64, e tuiEvent) {
	e.generation = generation
	select {
	case m.events <- e:
	case <-m.ctx.Done():
	}
}
func (m *tuiModel) connect() tea.Cmd {
	t, err := protocol.Decode(strings.TrimSpace(m.input.Value()))
	if err != nil {
		m.message = err.Error()
		return nil
	}
	m.generation++
	generation := m.generation
	m.address = cleanText(t.URL)
	m.message = ""
	m.state = "connecting"
	m.input.Blur()
	token := strings.TrimSpace(m.input.Value())
	go func() {
		err := client.RunWithCallbacks(m.ctx, token, io.Discard, client.Callbacks{
			Connected: func() { m.send(generation, tuiEvent{connected: true}) },
			Command:   func(e client.CommandEvent) { m.send(generation, tuiEvent{command: &e}) },
			Output: func(id string, data []byte) {
				m.send(generation, tuiEvent{id: id, output: append([]byte(nil), data...)})
			},
		})
		m.send(generation, tuiEvent{finished: true, err: err})
	}()
	return m.waitEvent()
}
func (m *tuiModel) waitEvent() tea.Cmd {
	return func() tea.Msg {
		select {
		case e := <-m.events:
			return e
		case <-m.ctx.Done():
			return nil
		}
	}
}
func (m *tuiModel) appendLog(kind, s string) {
	if s == "" {
		return
	}
	m.logs += s
	if len(m.spans) > 0 && m.spans[len(m.spans)-1].kind == kind {
		m.spans[len(m.spans)-1].text += s
	} else {
		m.spans = append(m.spans, logSpan{kind, s})
	}
	if len(m.logs) > maxTUILog {
		excess := len(m.logs) - maxTUILog
		for excess > 0 && len(m.spans) > 0 {
			if len(m.spans[0].text) <= excess {
				excess -= len(m.spans[0].text)
				m.spans = m.spans[1:]
				continue
			}
			tail := m.spans[0].text[excess:]
			for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
				tail = tail[1:]
			}
			m.spans[0].text = tail
			excess = 0
		}
		var b strings.Builder
		for _, span := range m.spans {
			b.WriteString(span.text)
		}
		m.logs = b.String()
	}
	m.refreshLog()
}
func (m *tuiModel) refreshLog() {
	atBottom := m.viewport.AtBottom()
	var b strings.Builder
	for _, span := range m.spans {
		if style, ok := logStyles[span.kind]; ok {
			lines := strings.Split(span.text, "\n")
			for i, line := range lines {
				if line != "" {
					lines[i] = style.Render(line)
				}
			}
			b.WriteString(strings.Join(lines, "\n"))
		} else {
			b.WriteString(span.text)
		}
	}
	m.viewport.SetContent(ansi.Hardwrap(b.String(), max(1, m.viewport.Width), true))
	if atBottom {
		m.viewport.GotoBottom()
	}
}
func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		atBottom := m.viewport.AtBottom()
		m.viewport.Width = max(1, msg.Width-2)
		m.viewport.Height = max(1, msg.Height-5)
		m.refreshLog()
		if atBottom {
			m.viewport.GotoBottom()
		}
		m.input.Width = max(1, msg.Width-4)
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.cancel()
			return m, tea.Quit
		}
		if m.state == "disconnected" {
			if msg.String() == "enter" {
				return m, m.connect()
			}
		} else {
			switch msg.String() {
			case "q":
				m.cancel()
				return m, tea.Quit
			case "c":
				m.logs = ""
				m.spans = nil
				m.lastID = ""
				m.viewport.SetContent("")
				return m, nil
			}
		}
	case tuiEvent:
		if msg.generation != m.generation {
			return m, m.waitEvent()
		}
		if msg.connected {
			m.state = "connected"
			m.logs = ""
			m.spans = nil
			m.shells = make(map[string]int)
			m.nextShell = 0
			m.completed = 0
			m.lastID = ""
			m.decoders = make(map[string]*displayDecoder)
			m.viewport.SetContent("")
			if m.connectionProgress {
				m.appendLog("system", progressConnected+"\n")
			}
		}
		if e := msg.command; e != nil {
			if !e.Done {
				m.decoders[e.ID] = &displayDecoder{}
				m.nextShell++
				m.shells[e.ID] = m.nextShell
				m.appendLog("system", fmt.Sprintf("\ns%d  %s  $ ", m.nextShell, e.StartedAt.Format("15:04:05")))
				m.appendLog("command", cleanText(e.Command)+"\n")
				m.lastID = e.ID
			} else {
				m.completed++
				if e.Error != "" {
					m.switchSource(e.ID)
					m.appendLog("error", "\n"+cleanText(e.Error)+"\n")
				}
				delete(m.shells, e.ID)
				delete(m.decoders, e.ID)
			}
		}
		if len(msg.output) > 0 {
			if d := m.decoders[msg.id]; d != nil {
				s := d.text(string(msg.output))
				if s != "" {
					m.switchSource(msg.id)
					m.appendLog("output", s)
				}
			}
		}
		if msg.finished {
			m.state = "disconnected"
			m.input.Focus()
			if msg.err != nil {
				m.message = cleanText(msg.err.Error())
			} else {
				m.message = "Disconnected"
			}
			return m, textinput.Blink
		}
		return m, m.waitEvent()
	}
	var cmd tea.Cmd
	if m.state == "disconnected" {
		m.input, cmd = m.input.Update(msg)
	} else {
		m.viewport, cmd = m.viewport.Update(msg)
	}
	return m, cmd
}
func (m *tuiModel) switchSource(id string) {
	if m.lastID != id {
		if number, ok := m.shells[id]; ok {
			m.appendLog("system", fmt.Sprintf("\ns%d\n", number))
		}
		m.lastID = id
	}
}
func (m *tuiModel) View() string {
	view := m.view()
	if m.width <= 0 {
		return view
	}
	lines := strings.Split(view, "\n")
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.width, "")
	}
	return strings.Join(lines, "\n")
}
func (m *tuiModel) view() string {
	status := "Disconnected"
	if m.state == "connecting" {
		status = "Connecting"
	}
	if m.state == "connected" {
		status = "Connected"
	}
	if m.connectionProgress {
		switch m.state {
		case "connecting":
			status = "[4/5] 正在连接…"
		case "connected":
			status = progressConnected
		}
	}
	if style, ok := statusStyles[m.state]; ok {
		status = style.Render(status)
	}
	header := m.header(status)
	if m.state == "disconnected" {
		return header + "\n\n Connection token:\n " + m.input.View() + "\n\n " + m.message + "\n\n Enter connect  ·  Ctrl+C quit"
	}
	line := strings.Repeat("─", max(1, m.width-2))
	return header + "\n " + line + "\n" + m.viewport.View() + "\n " + line + fmt.Sprintf("\n %d commands  ·  ↑↓ scroll  ·  c clear  ·  q quit", m.completed)
}
func (m *tuiModel) header(status string) string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	left := " Agenterm"
	// Keep the status visible when the terminal is too narrow for the brand.
	if width < ansi.StringWidth(left)+1+ansi.StringWidth(status) {
		return ansi.Truncate(status, width, "")
	}
	right := status
	available := width - ansi.StringWidth(left) - 2 // gap and right margin
	if m.state != "disconnected" && m.address != "" {
		addressWidth := available - ansi.StringWidth(status) - 5 // separator
		if addressWidth > 0 {
			right = ansi.Truncate(m.address, addressWidth, "…") + "  ·  " + status
		}
	}
	gap := max(1, width-ansi.StringWidth(left)-ansi.StringWidth(right)-1)
	return left + strings.Repeat(" ", gap) + right + " "
}
func cleanText(s string) string { return (&displayDecoder{}).text(s) }

// displayDecoder strips terminal control sequences while preserving split UTF-8.
type displayDecoder struct {
	state   byte
	pending string
}

func (d *displayDecoder) text(chunk string) string {
	chunk = d.pending + chunk
	d.pending = ""
	var out strings.Builder
	for len(chunk) > 0 {
		if !utf8.FullRuneInString(chunk) {
			d.pending = chunk
			break
		}
		r, n := utf8.DecodeRuneInString(chunk)
		chunk = chunk[n:]
		switch d.state {
		case 1:
			switch r {
			case '[':
				d.state = 2
			case ']':
				d.state = 3
			default:
				d.state = 0
			}
		case 2:
			if r >= 0x40 && r <= 0x7e {
				d.state = 0
			}
		case 3:
			if r == 7 {
				d.state = 0
			} else if r == 27 {
				d.state = 4
			}
		case 4:
			if r == '\\' {
				d.state = 0
			} else {
				d.state = 3
			}
		default:
			if r == 27 {
				d.state = 1
			} else if r == '\n' || r == '\t' || r >= 32 && r != 127 {
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}
