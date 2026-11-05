package main

import (
	"context"
	"fmt"
	"io"
	"os"
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
)

const maxTUILog = 10 * 1024 * 1024

type logSpan struct {
	kind, text string
	expanded   bool
	layout     *tuiLogLayout
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
	input                   textinput.Model
	viewport                viewport.Model
	events                  chan tuiEvent
	ctx                     context.Context
	cancel                  context.CancelFunc
	state, address, message string
	completed               int
	generation              uint64
	decoders                map[string]*logview.Decoder
	spans                   []logSpan
	logBytes                int
	folds                   []tuiFold
	logRows                 []string
	softWraps               map[int]bool
	logDirty                bool
	batchingLogs            bool
	logLayoutStyle          tuiLogLayoutStyle
	selection               *tuiSelection
	selectionID             uint64
	clipboard               *tuiClipboard
	legacyCells             bool
	lastID                  string
	width, height           int
	connectionProgress      bool
	temporary               bool
	autoConnect             bool
	connectionCancel        context.CancelFunc
	invitation              string
	spacing                 logview.Spacing
	latency                 time.Duration
	retryDelay              time.Duration
	everConnected           bool
	serverChoices           []string
	serverIndex             int
	selectingServer         bool
	selectModeAfterServer   bool
	selectingMode           bool
	modeIndex               int
}

func newTUI(token string) *tuiModel {
	ctx, cancel := context.WithCancel(context.Background())
	input := textinput.New()
	input.Placeholder = tr("paste.connection.token")
	input.CharLimit = 4096
	input.SetValue(token)
	input.Focus()
	return &tuiModel{input: input, viewport: viewport.New(80, 15), events: make(chan tuiEvent, 256), ctx: ctx, cancel: cancel, state: "disconnected", latency: -1, decoders: make(map[string]*logview.Decoder), clipboard: &tuiClipboard{write: writeTUIClipboard}, legacyCells: os.Getenv("TERM_PROGRAM") == "Apple_Terminal"}
}
func runTUI(token string, connectionProgress, tokenProvided, temporary bool) error {
	m := newTUI(token)
	m.connectionProgress = connectionProgress
	m.temporary = temporary
	m.autoConnect = tokenProvided
	if connectionProgress {
		m.invitation = token
	}
	defer m.cancel()
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}
func (m *tuiModel) Init() tea.Cmd {
	if m.input.Value() != "" || m.autoConnect {
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
func (m *tuiModel) beginConnect() tea.Cmd {
	value := strings.TrimSpace(m.input.Value())
	if client.IsShortCode(value) {
		m.selectModeAfterServer = true
		cmd := m.connect()
		if !m.selectingServer {
			m.selectModeAfterServer = false
		}
		return cmd
	}
	if _, _, err := client.ConnectionAddress(value); err != nil {
		m.message = client.TokenValidationError().Error()
		return nil
	}
	m.selectingMode, m.modeIndex, m.message = true, 0, ""
	m.input.Blur()
	return nil
}
func (m *tuiModel) connect() tea.Cmd {
	value := strings.TrimSpace(m.input.Value())
	if client.IsShortCode(value) {
		servers, err := client.ServerHistory()
		if err != nil {
			m.message = tr("history.server.failed")
			return nil
		}
		if len(servers) == 0 {
			m.message = tr("history.server.empty")
			return nil
		}
		m.serverChoices, m.serverIndex, m.selectingServer, m.message = servers, 0, true, ""
		return nil
	}
	address, identifier, err := client.ConnectionAddress(value)
	if err != nil {
		m.message = client.TokenValidationError().Error()
		return nil
	}
	m.generation++
	generation := m.generation
	m.address = cleanText(address)
	if identifier != "" {
		m.address += "/" + identifier
	}
	m.message = ""
	m.state = "connecting"
	m.latency = -1
	m.everConnected = false
	m.logBytes, m.lastID = 0, ""
	m.spans = nil
	m.folds = nil
	m.selection = nil
	m.logRows, m.softWraps, m.logDirty = nil, nil, false
	m.completed = 0
	m.spacing = logview.Spacing{}
	m.viewport.SetContent("")
	m.viewport.GotoTop()
	m.input.Blur()
	token := strings.TrimSpace(m.input.Value())
	run := client.RunWithCallbacks
	if token != "" && token == m.invitation {
		run = client.RunInvitationWithCallbacks
	}
	connectionCtx, connectionCancel := context.WithCancel(m.ctx)
	m.connectionCancel = connectionCancel
	temporary := m.temporary
	go func() {
		defer connectionCancel()
		idleTimeout := time.Duration(0)
		if temporary {
			idleTimeout = 5 * time.Minute
		}
		idleExpired := false
		err := run(connectionCtx, token, io.Discard, client.Callbacks{
			IdleTimeout:      idleTimeout,
			IdleDisconnected: func() { idleExpired = true },
			Redeemed: func(token string) {
				if target, _, err := client.ConnectionAddress(token); err == nil {
					address = target
				}
				m.send(generation, tuiEvent{redeemed: token})
			},
			Connecting:   func() { m.send(generation, tuiEvent{connecting: true}) },
			Connected:    func() { _ = client.RememberServer(address); m.send(generation, tuiEvent{connected: true}) },
			Reconnecting: func(e client.ReconnectEvent) { m.send(generation, tuiEvent{retry: &e}) },
			Latency:      func(d time.Duration) { m.send(generation, tuiEvent{latency: &d}) },
			Command:      func(e client.CommandEvent) { m.send(generation, tuiEvent{command: &e}) },
			Output: func(id string, data []byte) {
				m.send(generation, tuiEvent{id: id, output: append([]byte(nil), data...)})
			},
		})
		if idleExpired {
			err = fmt.Errorf("%s", tr("connection.idle.disconnected"))
		}
		m.send(generation, tuiEvent{finished: true, err: err})
	}()
	return m.waitEvent()
}
func (m *tuiModel) waitEvent() tea.Cmd {
	return func() tea.Msg {
		select {
		case e := <-m.events:
			if len(e.output) == 0 {
				return e
			}
			// Collect one frame of output, preserving command/source order.
			// Lifecycle events end the frame immediately; batches stay bounded.
			batch := tuiLogBatch{e}
			size := len(e.output)
			timer := time.NewTimer(time.Second / 60)
			defer timer.Stop()
			for len(batch) < 256 && size < 64<<10 {
				select {
				case next := <-m.events:
					batch = append(batch, next)
					size += len(next.output)
					if len(next.output) == 0 {
						return batch
					}
				case <-timer.C:
					return batch
				case <-m.ctx.Done():
					return batch
				}
			}
			return batch
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
	m.logBytes += len(s)
	if len(m.spans) > 0 && kind != "system" && m.spans[len(m.spans)-1].kind == kind {
		m.spans[len(m.spans)-1].text += s
	} else {
		m.spans = append(m.spans, logSpan{kind: kind, text: s})
	}
	if m.logBytes > maxTUILog {
		// Discard coordinates into history that is about to be evicted.
		m.selection = nil
		excess := m.logBytes - maxTUILog
		for excess > 0 && len(m.spans) > 0 {
			if len(m.spans[0].text) <= excess {
				excess -= len(m.spans[0].text)
				m.logBytes -= len(m.spans[0].text)
				m.spans[0] = logSpan{}
				m.spans = m.spans[1:]
				continue
			}
			tail := m.spans[0].text[excess:]
			for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
				tail = tail[1:]
			}
			m.logBytes -= len(m.spans[0].text) - len(tail)
			m.spans[0].text = strings.Clone(tail)
			excess = 0
		}
	}
	if m.batchingLogs {
		m.logDirty = true
	} else {
		m.refreshLog()
	}
}

type tuiLogBatch []tuiEvent

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tuiLogBatch:
		m.batchingLogs = true
		var cmd tea.Cmd
		for _, event := range msg {
			_, cmd = m.Update(event)
		}
		m.batchingLogs = false
		if m.logDirty {
			m.refreshLog()
		}
		return m, cmd
	case tea.WindowSizeMsg:
		// Rewrapping changes content coordinates; dismiss the old selection.
		m.selection = nil
		m.width, m.height = msg.Width, msg.Height
		atBottom := m.viewport.AtBottom()
		m.viewport.Width = max(1, msg.Width-2)
		m.viewport.Height = max(1, msg.Height-5)
		m.refreshLog()
		if atBottom {
			m.viewport.GotoBottom()
		}
		// Reserve the form's left margin, prompt and cursor cell.
		m.input.Width = max(1, m.viewport.Width-m.cellWidth(m.input.Prompt)-2)
	case tea.MouseMsg:
		return m, m.handleLogMouse(msg)
	case tuiSelectionTick:
		return m, m.scrollSelection(uint64(msg))
	case tuiCopyResult:
		if m.selection != nil && msg.selectionID == m.selectionID && msg.request == m.clipboard.request.Load() {
			m.selection.notice = msg.notice
			if msg.err != nil {
				m.selection.notice = "tui.selection.copy.failed"
			}
		}
		return m, nil
	case tea.KeyMsg:
		if m.selectingMode {
			switch msg.String() {
			case "left", "up":
				m.modeIndex = 0
			case "right", "down":
				m.modeIndex = 1
			case "esc", "ctrl+c":
				m.selectingMode = false
				m.input.Focus()
			case "enter":
				m.selectingMode = false
				m.temporary = m.modeIndex == 0
				return m, m.connect()
			}
			return m, nil
		}
		if m.selectingServer {
			switch msg.String() {
			case "up", "k":
				if m.serverIndex > 0 {
					m.serverIndex--
				}
			case "down", "j":
				if m.serverIndex+1 < len(m.serverChoices) {
					m.serverIndex++
				}
			case "esc", "ctrl+c":
				m.selectingServer = false
				m.selectModeAfterServer = false
			case "enter":
				address, err := client.ShortCodeForServer(m.serverChoices[m.serverIndex], m.input.Value())
				m.selectingServer = false
				if err != nil {
					m.message = client.TokenValidationError().Error()
					return m, nil
				}
				m.input.SetValue(address)
				if m.selectModeAfterServer {
					m.selectModeAfterServer = false
					return m, m.beginConnect()
				}
				return m, m.connect()
			}
			return m, nil
		}
		if m.selection != nil {
			if (msg.String() == "ctrl+c" || msg.String() == "ctrl+shift+c") && m.selection.dragged {
				return m, m.copySelection()
			}
			// Scrolling retains the selection. Other keys resume live rendering.
			switch msg.String() {
			case "up", "down", "pgup", "pgdown", "home", "end":
			default:
				m.clearSelection()
			}
			if msg.String() == "esc" {
				return m, nil
			}
		}
		if msg.String() == "ctrl+c" {
			if m.state == "disconnected" {
				if m.input.Value() != "" {
					m.input.SetValue("")
					m.invitation = ""
					m.message = ""
					return m, nil
				}
				m.cancel()
				return m, tea.Quit
			}
			if m.connectionCancel != nil {
				m.connectionCancel()
				m.connectionCancel = nil
			}
			m.state = "disconnecting"
			return m, nil
		}
		if m.state == "disconnected" && msg.String() == "enter" {
			return m, m.beginConnect()
		}
	case tuiEvent:
		if msg.generation != m.generation {
			return m, m.waitEvent()
		}
		if msg.redeemed != "" {
			if target, identifier, err := client.ConnectionAddress(msg.redeemed); err == nil {
				m.address = cleanText(target)
				if identifier != "" {
					m.address += "/" + identifier
				}
			}
			m.input.SetValue(msg.redeemed)
			m.invitation = ""
		}
		if msg.connected {
			m.state = "connected"
			m.latency = -1
			m.lastID = ""
			m.decoders = make(map[string]*logview.Decoder)
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
				m.decoders[e.ID] = &logview.Decoder{}
				m.appendLog("system", m.spacing.Before(2)+e.StartedAt.Format("15:04:05")+" $ ")
				m.appendLog("command", cleanText(e.Command)+"\n")
				m.lastID = e.ID
			} else {
				m.completed++
				if e.Error != "" {
					m.switchSource(e.ID)
					m.appendLog("error", m.spacing.Before(1)+cleanText(e.Error)+"\n")
				}
				delete(m.decoders, e.ID)
			}
		}
		if len(msg.output) > 0 {
			if d := m.decoders[msg.id]; d != nil {
				s := d.Output.Text(d.Text(string(msg.output)))
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
			m.connectionCancel = nil
			m.clearSelection()
			m.state = "disconnected"
			m.latency = -1
			m.input.Focus()
			if msg.err != nil {
				m.message = cleanText(msg.err.Error())
			} else {
				m.message = tr("disconnected.2")
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
		if m.decoders[id] != nil {
			m.appendLog("system", m.spacing.Before(2))
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
		lines[i] = m.truncateCells(lines[i], m.width, "")
	}
	return strings.Join(lines, "\n")
}
func (m *tuiModel) view() string {
	status := tr("disconnected.2")
	if m.state == "connecting" {
		status = tr("connecting")
	}
	if m.state == "connected" {
		status = tr("connected.persistent")
		if m.temporary {
			status = tr("connected.temporary")
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
	content := m.selectionView()
	help := "\n " + m.connectedFooter()
	if m.state == "disconnected" {
		content = m.connectionView()
		help = "\n " + tr("enter.connect.ctrl.c.quit")
		if m.selectingMode {
			help = "\n " + tr("connection.mode.help")
		}
	}
	line := strings.Repeat("─", max(1, m.width-2))
	if m.selection != nil && m.selection.dragged {
		selectionHelp := tr("tui.selection.help")
		if m.selection.notice != "" {
			selectionHelp = tr(m.selection.notice) + " · " + selectionHelp
		}
		help = "\n " + m.footerWithLeft(selectionHelp)
	}
	return header + "\n " + line + "\n" + content + "\n " + line + help
}
func (m *tuiModel) connectedFooter() string {
	return m.footerWithLeft(tr("tui.ctrl.c.quit"))
}
func (m *tuiModel) footerWithLeft(left string) string {
	right := fmt.Sprintf(tr("tui.command.count"), m.completed)
	if m.state == "connected" && m.latency >= 0 {
		right += fmt.Sprintf(" · %dms", max(1, m.latency.Milliseconds()))
	}
	width := max(1, m.width-2)
	if m.cellWidth(left)+m.cellWidth(right)+1 > width {
		left = m.truncateCells(left, max(0, width-m.cellWidth(right)-1), "…")
	}
	space := max(1, width-m.cellWidth(left)-m.cellWidth(right))
	return left + strings.Repeat(" ", space) + right
}
func (m *tuiModel) connectionView() string {
	content := " " + tr("connection.token") + "\n " + m.input.View()
	if m.selectingMode {
		choices := []string{tr("connection.mode.temporary"), tr("connection.mode.persistent")}
		for i := range choices {
			if i == m.modeIndex {
				choices[i] = statusStyles["connected"].Reverse(true).Render(" ▶ " + choices[i] + " ")
			} else {
				choices[i] = "   " + choices[i] + " "
			}
		}
		separator := "   "
		if m.cellWidth(strings.Join(choices, separator))+1 > m.viewport.Width {
			separator = "\n "
		}
		content += "\n\n " + tr("connection.mode.select") + "\n " + strings.Join(choices, separator)
	}
	if m.selectingServer {
		content += "\n\n " + tr("select.history.server")
		available := max(1, m.viewport.Height-5)
		start := max(0, m.serverIndex-available+1)
		for i := start; i < len(m.serverChoices) && i < start+available; i++ {
			marker := "  "
			if i == m.serverIndex {
				marker = "> "
			}
			content += "\n " + marker + cleanText(m.serverChoices[i])
		}
		content += "\n " + tr("history.server.select.help")
	}
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
	if width < m.cellWidth(left)+1+m.cellWidth(status) {
		return m.truncateCells(status, width, "")
	}
	right := status
	available := width - m.cellWidth(left) - 2 // gap and right margin
	if m.state != "disconnected" && m.address != "" {
		addressWidth := available - m.cellWidth(status) - 5 // separator
		if addressWidth > 0 {
			address := m.address
			i := strings.LastIndex(address, "/client/")
			base, identifier, found := "", "", i >= 0
			if found {
				base, identifier = address[:i], address[i+len("/client/"):]
			}
			if found && protocol.ValidTokenIdentifier(identifier) {
				suffix := "/" + identifier
				if addressWidth > m.cellWidth(suffix) {
					address = m.truncateCells(base+"/client", addressWidth-m.cellWidth(suffix), "…") + suffix
				} else {
					address = m.truncateCells(identifier, addressWidth, "…")
				}
			} else {
				address = m.truncateCells(address, addressWidth, "…")
			}
			right = address + "  ·  " + status
		}
	}
	gap := max(1, width-m.cellWidth(left)-m.cellWidth(right)-1)
	return left + strings.Repeat(" ", gap) + right + " "
}
func cleanText(s string) string { return (&logview.Decoder{}).Text(s) }
