package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"agenterm/internal/client"
	"agenterm/internal/logview"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const maxTUILog = 10 * 1024 * 1024

type logSpan struct {
	kind, text string
	expanded   bool
}

type tuiFold struct {
	span, start, end int // Rendered row range, excluding surrounding blank lines.
}

var logStyles = map[string]lipgloss.Style{
	"system":  lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#4b677a", Dark: "#99b1c3"}),
	"command": lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#257445", Dark: "#85c99b"}),
	"error":   lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#c53030", Dark: "#ff8a8a"}),
}

var statusStyles = map[string]lipgloss.Style{
	"connected":    lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#257445", Dark: "#85c99b"}),
	"connecting":   lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#986000", Dark: "#e6be6a"}),
	"reconnecting": lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#986000", Dark: "#e6be6a"}),
	"disconnected": lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#737373", Dark: "#a3a3a3"}),
}

type tuiEvent struct {
	generation uint64
	command    *client.CommandEvent
	id         string
	output     []byte
	connected  bool
	connecting bool
	retry      *client.ReconnectEvent
	latency    *time.Duration
	redeemed   string
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
	folds                         []tuiFold
	selecting                     bool
	selectionView                 string
	lastID                        string
	width, height                 int
	connectionProgress            bool
	invitation                    string
	spacing                       logview.Spacing
	latency                       time.Duration
	retryDelay                    time.Duration
	everConnected                 bool
}

func newTUI(token string) *tuiModel {
	ctx, cancel := context.WithCancel(context.Background())
	input := textinput.New()
	input.Placeholder = tr("paste.connection.token")
	input.CharLimit = 4096
	input.SetValue(token)
	input.Focus()
	return &tuiModel{input: input, viewport: viewport.New(80, 15), events: make(chan tuiEvent, 256), ctx: ctx, cancel: cancel, state: "disconnected", latency: -1, decoders: make(map[string]*displayDecoder), shells: make(map[string]int)}
}
func runTUI(token string, connectionProgress bool) error {
	m := newTUI(token)
	m.connectionProgress = connectionProgress
	if connectionProgress {
		m.invitation = token
	}
	defer m.cancel()
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
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
	m.latency = -1
	m.everConnected = false
	m.logs, m.lastID = "", ""
	m.spans = nil
	m.folds = nil
	m.completed, m.nextShell = 0, 0
	m.spacing = logview.Spacing{}
	m.viewport.SetContent("")
	m.viewport.GotoTop()
	m.input.Blur()
	token := strings.TrimSpace(m.input.Value())
	run := client.RunWithCallbacks
	if token != "" && token == m.invitation {
		run = client.RunInvitationWithCallbacks
	}
	go func() {
		err := run(m.ctx, token, io.Discard, client.Callbacks{
			Redeemed:     func(token string) { m.send(generation, tuiEvent{redeemed: token}) },
			Connecting:   func() { m.send(generation, tuiEvent{connecting: true}) },
			Connected:    func() { m.send(generation, tuiEvent{connected: true}) },
			Reconnecting: func(e client.ReconnectEvent) { m.send(generation, tuiEvent{retry: &e}) },
			Latency:      func(d time.Duration) { m.send(generation, tuiEvent{latency: &d}) },
			Command:      func(e client.CommandEvent) { m.send(generation, tuiEvent{command: &e}) },
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
	m.spacing.Observe(s)
	m.logs += s
	if len(m.spans) > 0 && kind != "system" && m.spans[len(m.spans)-1].kind == kind {
		m.spans[len(m.spans)-1].text += s
	} else {
		m.spans = append(m.spans, logSpan{kind: kind, text: s})
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
	if !m.selecting {
		m.refreshLog()
	}
}
func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		atBottom := m.viewport.AtBottom()
		m.viewport.Width = max(1, msg.Width-2)
		m.viewport.Height = max(1, msg.Height-5)
		if !m.selecting {
			m.refreshLog()
		}
		if atBottom {
			m.viewport.GotoBottom()
		}
		// Reserve the form's left margin, prompt and cursor cell.
		m.input.Width = max(1, m.viewport.Width-ansi.StringWidth(m.input.Prompt)-2)
	case tea.MouseMsg:
		// The log begins below the header and divider, at screen row 2.
		if m.selecting || m.state == "disconnected" || msg.X < 0 || msg.X >= m.viewport.Width || msg.Y < 2 || msg.Y >= 2+m.viewport.Height {
			return m, nil
		}
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			m.toggleFold(m.viewport.YOffset + msg.Y - 2)
			return m, nil
		}
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
			case "s":
				m.selecting = !m.selecting
				if m.selecting {
					// Print a stable, unfolded snapshot into the terminal's main
					// buffer so its native scrollback and cross-screen selection work.
					m.selectionView = tr("tui.selection.help")
					return m, tea.Sequence(tea.DisableMouse, tea.ExitAltScreen, tea.Println(strings.TrimSuffix(m.logs, "\n")))
				}
				m.selectionView = ""
				m.refreshLog()
				return m, tea.Sequence(tea.EnterAltScreen, tea.EnableMouseCellMotion)
			case "c":
				if m.selecting {
					return m, nil
				}
				m.logs = ""
				m.spans = nil
				m.folds = nil
				m.lastID = ""
				m.spacing = logview.Spacing{}
				for _, decoder := range m.decoders {
					decoder.output = logview.Output{}
				}
				m.viewport.SetContent("")
				m.viewport.GotoTop()
				return m, nil
			}
			if m.selecting {
				return m, nil
			}
		}
	case tuiEvent:
		if msg.generation != m.generation {
			return m, m.waitEvent()
		}
		if msg.redeemed != "" {
			m.input.SetValue(msg.redeemed)
			m.invitation = ""
		}
		if msg.connected {
			m.state = "connected"
			m.latency = -1
			m.shells = make(map[string]int)
			m.lastID = ""
			m.decoders = make(map[string]*displayDecoder)
			if m.everConnected {
				m.appendLog("system", m.spacing.Before(2)+time.Now().Format("15:04:05")+" "+tr("connection.restored")+"\n")
			} else if m.connectionProgress {
				m.appendLog("system", progressConnected+"\n")
			}
			m.everConnected = true
		}
		if msg.connecting {
			m.state, m.latency = "connecting", -1
		}
		if msg.retry != nil {
			m.state, m.latency, m.retryDelay = "reconnecting", -1, msg.retry.Delay
			m.appendLog("error", m.spacing.Before(2)+time.Now().Format("15:04:05")+" "+cleanText(msg.retry.Text(cliLanguage))+"\n")
		}
		if msg.latency != nil && m.state == "connected" {
			m.latency = *msg.latency
		}
		if e := msg.command; e != nil {
			if !e.Done {
				m.decoders[e.ID] = &displayDecoder{}
				m.nextShell++
				m.shells[e.ID] = m.nextShell
				m.appendLog("system", m.spacing.Before(2)+fmt.Sprintf("s%d %s $ ", m.nextShell, e.StartedAt.Format("15:04:05")))
				m.appendLog("command", cleanText(e.Command)+"\n")
				m.lastID = e.ID
			} else {
				m.completed++
				if e.Error != "" {
					m.switchSource(e.ID)
					m.appendLog("error", m.spacing.Before(1)+cleanText(e.Error)+"\n")
				}
				delete(m.shells, e.ID)
				delete(m.decoders, e.ID)
			}
		}
		if len(msg.output) > 0 {
			if d := m.decoders[msg.id]; d != nil {
				s := d.output.Text(d.text(string(msg.output)))
				if m.lastID != msg.id {
					s = logview.TrimLeadingBlankLines(s)
				}
				if s != "" {
					m.switchSource(msg.id)
					m.appendLog("output", s)
				}
			}
		}
		if msg.finished {
			m.state = "disconnected"
			m.latency = -1
			m.input.Focus()
			if msg.err != nil {
				m.message = cleanText(msg.err.Error())
			} else {
				m.message = tr("disconnected.2")
			}
			if m.selecting {
				m.selecting, m.selectionView = false, ""
				return m, tea.Sequence(tea.EnterAltScreen, tea.EnableMouseCellMotion, textinput.Blink)
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
			m.appendLog("system", m.spacing.Before(2)+fmt.Sprintf("s%d\n", number))
		}
		m.lastID = id
	}
}
func (m *tuiModel) View() string {
	if m.selectionView != "" {
		if m.width > 0 {
			return ansi.Truncate(m.selectionView, m.width, "")
		}
		return m.selectionView
	}
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
	status := tr("disconnected.2")
	if m.state == "connecting" {
		status = tr("connecting")
	}
	if m.state == "connected" {
		status = tr("connected.2")
		if m.latency >= 0 {
			status += fmt.Sprintf(" · %dms", max(1, m.latency.Milliseconds()))
		}
	}
	if m.state == "reconnecting" {
		status = fmt.Sprintf(tr("connection.retry.status"), int(m.retryDelay.Seconds()))
	}
	if m.connectionProgress {
		switch m.state {
		case "connecting":
			status = tr("3.4.connecting")
		}
	}
	if style, ok := statusStyles[m.state]; ok {
		status = style.Render(status)
	}
	header := m.header(status)
	content := m.viewport.View()
	help := fmt.Sprintf(tr("d.commands.scroll.c.clear.q.quit"), m.completed)
	if m.state == "disconnected" {
		content = m.connectionView()
		help = "\n " + tr("enter.connect.ctrl.c.quit")
	}
	line := strings.Repeat("─", max(1, m.width-2))
	if m.selecting {
		help = "\n " + tr("tui.selection.help")
	}
	return header + "\n " + line + "\n" + content + "\n " + line + help
}
func (m *tuiModel) connectionView() string {
	content := " " + tr("connection.token") + "\n " + m.input.View()
	if m.message != "" {
		content += "\n\n " + logStyles["error"].Render(m.message)
	}
	return lipgloss.NewStyle().
		Width(m.viewport.Width).
		Height(m.viewport.Height).
		MaxWidth(m.viewport.Width).
		MaxHeight(m.viewport.Height).
		Render(content)
}
func (m *tuiModel) header(status string) string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	left := " Agenterm v" + version.Current
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
	output  logview.Output
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
