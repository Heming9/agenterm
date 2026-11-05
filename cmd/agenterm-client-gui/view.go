package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"agenterm/internal/client"
	"agenterm/internal/i18n"
	"agenterm/internal/logview"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type commandStats struct {
	completed int
	total     time.Duration
}

func (s *commandStats) apply(event client.CommandEvent) {
	if !event.Done {
		return
	}
	s.completed++
	s.total += event.Duration
}

func (s *commandStats) text() string {
	average := "—"
	if s.completed > 0 {
		average = durationText(s.total / time.Duration(s.completed))
	}
	return fmt.Sprintf(tr("d.commands.average.s"), s.completed, average)
}

func durationText(d time.Duration) string {
	if d < time.Millisecond {
		return "<1 ms"
	}
	if d < time.Second {
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.2f s", d.Seconds())
}

type clientView struct {
	window  fyne.Window
	content fyne.CanvasObject

	token                           *widget.Entry
	connectButton, disconnectButton *prototypeButton
	address, status, statistics     *uiText
	inputRow, connectedRow          *fyne.Container
	header, historyHeader           *fyne.Container
	statusBadge                     *surface
	connectionError                 *widget.Label
	terminal                        *fyne.Container
	logColors                       *widget.RichText
	logSelection                    *widget.Label
	emptyTitle, emptyDetail         *uiText
	terminalTheme                   *monochromeTheme
	terminalPanel                   *container.ThemeOverride
	empty                           *fyne.Container
	scroll                          *container.Scroll

	buffer       *logBuffer
	logText      string
	logSpans     []logSpan
	decoder      logview.Decoder
	stats        commandStats
	cancel       context.CancelFunc
	connected    bool
	connecting   bool
	closed       bool
	generation   uint64
	ticker       *time.Ticker
	done         chan struct{}
	invitation   string
	serverDialog dialog.Dialog
	latency      time.Duration
}

func padded(object fyne.CanvasObject, top, bottom, left, right float32) *fyne.Container {
	return container.New(layout.NewCustomPaddedLayout(top, bottom, left, right), object)
}

func mutedLabel(text string) *uiText {
	return newText(text, 12, theme.ColorNamePlaceHolder)
}

func newClientView(win fyne.Window) *clientView {
	v := &clientView{window: win, buffer: &logBuffer{}, done: make(chan struct{})}
	v.token = widget.NewEntry()
	v.token.SetPlaceHolder(tr("paste.the.connection.token.from.the.server"))
	v.token.OnSubmitted = func(string) { v.connect() }
	v.connectButton = newButton(tr("connect"), arrowIcon, func() {
		if v.connecting {
			v.disconnect()
		} else {
			v.connect()
		}
	})
	v.connectButton.IconPlacement = widget.ButtonIconTrailingText
	v.connectButton.Importance = widget.HighImportance
	v.disconnectButton = newButton(tr("disconnect"), unplugIcon, v.disconnect)
	v.address = mutedLabel("")
	v.address.TextStyle.Monospace = true
	v.status = newText(tr("connected"), 12, colourTagText)
	v.statusBadge = newSurface(padded(v.status, 4, 4, 10, 10), 6)
	v.statusBadge.fill, v.statusBadge.border = colourTag, false
	connectControl := container.NewThemeOverride(v.connectButton, buttonTheme{newMonochromeTheme(false)})
	disconnectControl := container.NewThemeOverride(v.disconnectButton, buttonTheme{newMonochromeTheme(false)})
	tokenControl := container.NewThemeOverride(v.token, entryTheme{Theme: newMonochromeTheme(false), height: 38})
	v.inputRow = container.NewBorder(nil, nil, nil, padded(slot(connectControl, 110, 38, false), 0, 0, 8, 0), tokenControl)
	v.connectedRow = container.NewBorder(nil, nil, nil,
		container.New(layout.NewCustomPaddedHBoxLayout(14), v.address, container.NewCenter(v.statusBadge),
			slot(newSurface(disconnectControl, 7), 130, 38, false)), nil)
	v.connectedRow.Hide()
	connection := container.NewStack(slot(v.inputRow, 440, 38, true), v.connectedRow)

	logo := canvas.NewImageFromResource(fyne.NewStaticResource("app-icon.png", appIcon))
	logo.FillMode = canvas.ImageFillContain
	title := newText("Agenterm", 20, theme.ColorNameForeground)
	var brandTitle fyne.CanvasObject = title
	if version := strings.TrimSpace(fyne.CurrentApp().Metadata().Version); version != "" {
		versionLabel := newText("v"+strings.TrimPrefix(version, "v"), 10, theme.ColorNamePlaceHolder)
		versionLabel.Alignment = fyne.TextAlignTrailing
		brandTitle = container.New(layout.NewCustomPaddedVBoxLayout(-6), title, versionLabel)
	}
	brand := padded(container.New(layout.NewCustomPaddedHBoxLayout(10),
		container.NewCenter(slot(logo, 32, 32, false)), brandTitle), 0, 0, 0, 24)
	v.header = container.NewBorder(nil, nil, brand, nil, connection)
	v.connectionError = widget.NewLabel("")
	v.connectionError.Importance = widget.DangerImportance
	v.connectionError.Wrapping = fyne.TextWrapWord
	v.connectionError.Hide()
	heading := container.NewVBox(padded(slot(v.header, 0, 38, false), 0, 18, 0, 0), v.connectionError, widget.NewSeparator())

	v.statistics = mutedLabel(v.stats.text())
	clearButton := newButton(tr("clear"), trashIcon, v.clearLogs)
	clearButton.Importance = widget.LowImportance
	clearControl := container.NewThemeOverride(clearButton, compactControlTheme{newMonochromeTheme(false)})
	historyTitle := newText(tr("history"), 14, theme.ColorNameForeground)
	v.historyHeader = container.NewBorder(nil, nil, historyTitle,
		container.New(layout.NewCustomPaddedHBoxLayout(12), v.statistics,
			container.NewCenter(slot(widget.NewSeparator(), 1, 16, false)), clearControl), nil)

	v.logColors = widget.NewRichText()
	v.logColors.Wrapping = fyne.TextWrapBreak
	v.logSelection = widget.NewLabel("")
	v.logSelection.Selectable = true
	v.logSelection.Wrapping = fyne.TextWrapBreak
	selectionOverlay := container.NewThemeOverride(v.logSelection, logSelectionTheme{newMonochromeTheme(true)})
	v.terminal = container.NewStack(v.logColors, selectionOverlay)
	v.scroll = container.NewVScroll(padded(v.terminal, 20, 20, 20, 20))
	v.scroll.SetMinSize(fyne.NewSize(0, 180))
	v.scroll.Hide()
	v.emptyTitle = mutedLabel(tr("waiting.for.connection"))
	v.emptyTitle.Alignment = fyne.TextAlignCenter
	v.emptyDetail = mutedLabel(tr("waiting.for.the.next.command.from.the"))
	v.emptyDetail.Alignment = fyne.TextAlignCenter
	v.emptyDetail.Hide()
	v.empty = container.NewCenter(container.New(layout.NewCustomPaddedVBoxLayout(7),
		container.NewCenter(slot(widget.NewIcon(theme.NewColoredResource(terminalIcon, theme.ColorNamePlaceHolder)), 27, 27, false)),
		v.emptyTitle, v.emptyDetail))
	viewport := container.NewStack(v.scroll, v.empty)
	shell := client.Shell()
	toolbarTitle := newText(tr("commands.and.output"), 11, theme.ColorNamePlaceHolder)
	toolbarShell := newText(shell, 11, theme.ColorNamePlaceHolder)
	terminalHeader := container.NewBorder(nil, nil,
		container.New(layout.NewCustomPaddedHBoxLayout(8),
			container.NewCenter(slot(widget.NewIcon(theme.NewColoredResource(terminalIcon, theme.ColorNamePlaceHolder)), 15, 15, false)), toolbarTitle), toolbarShell, nil)
	terminalBody := container.NewBorder(
		container.NewVBox(padded(terminalHeader, 11, 11, 17, 17), widget.NewSeparator()),
		nil,
		nil, nil, viewport)
	v.terminalTheme = newMonochromeTheme(true).(*monochromeTheme)
	v.terminalTheme.inactive = true
	v.terminalPanel = container.NewThemeOverride(newSurface(terminalBody, 10), v.terminalTheme)
	records := container.NewBorder(padded(v.historyHeader, 20, 12, 0, 0), nil, nil, nil, v.terminalPanel)
	v.content = padded(container.NewBorder(heading, nil, nil, nil, records), 18, 26, 30, 30)
	return v
}

func (v *clientView) connect() {
	if v.closed || v.connecting || v.connected {
		return
	}
	value := strings.TrimSpace(v.token.Text)
	if client.IsShortCode(value) {
		if v.serverDialog != nil {
			return
		}
		servers, err := client.ServerHistory()
		if err != nil {
			v.showConnectionError(tr("history.server.failed"))
			return
		}
		if len(servers) == 0 {
			v.showConnectionError(tr("history.server.empty"))
			return
		}
		v.chooseServer(value, servers)
		return
	}
	v.connectValue(value, value != "" && value == v.invitation)
}

// serverSelectionList allows keyboard navigation and dismissing the picker
// without adding actions alongside the server choices.
type serverSelectionList struct {
	widget.List
	highlighted widget.ListItemID
	dismiss     func()
}

func (l *serverSelectionList) TypedKey(event *fyne.KeyEvent) {
	switch event.Name {
	case fyne.KeyEscape:
		l.dismiss()
	case fyne.KeyReturn, fyne.KeyEnter:
		l.Select(l.highlighted)
	default:
		l.List.TypedKey(event)
	}
}

func (v *clientView) chooseServer(code string, servers []string) {
	var picker *dialog.CustomDialog
	list := &serverSelectionList{}
	list.Length = func() int { return len(servers) }
	list.CreateItem = func() fyne.CanvasObject { return widget.NewLabel("") }
	list.UpdateItem = func(id widget.ListItemID, item fyne.CanvasObject) { item.(*widget.Label).SetText(servers[id]) }
	list.OnHighlighted = func(id widget.ListItemID) { list.highlighted = id }
	list.OnSelected = func(id widget.ListItemID) {
		if id < 0 || id >= len(servers) {
			return
		}
		address, err := client.ShortCodeForServer(servers[id], code)
		if err != nil {
			picker.Hide()
			v.showConnectionError(tr("history.server.choose"))
			return
		}
		picker.Hide()
		v.token.SetText(address)
		v.connectValue(address, false)
	}
	list.ExtendBaseWidget(list)
	picker = dialog.NewCustomWithoutButtons(tr("select.history.server"), list, v.window)
	list.dismiss = picker.Hide
	v.serverDialog = picker
	picker.SetOnClosed(func() { v.serverDialog = nil })
	picker.Resize(fyne.NewSize(440, float32(100+min(len(servers), 6)*40)))
	picker.Show()
	v.window.Canvas().Focus(list)
}

func (v *clientView) connectValue(value string, invitation bool) {
	if v.closed || v.connecting || v.connected {
		return
	}
	if invitation {
		v.invitation = value
	}
	address, identifier, err := client.ConnectionAddress(value)
	if err != nil {
		v.showConnectionError(client.TokenValidationError().Error())
		v.window.Canvas().Focus(v.token)
		return
	}
	serverURL, _ := url.Parse(address)
	// Show only the host, never the secret or any URL credentials/query string.
	host := serverURL.Host
	if identifier != "" {
		host += "/" + identifier
	}
	v.generation++
	current := v.generation
	ctx, stop := context.WithCancel(context.Background())
	v.cancel = stop
	v.connecting = true
	v.connectionError.Hide()
	v.connectButton.SetText(tr("cancel"))
	v.connectButton.SetIcon(theme.CancelIcon())
	v.connectButton.Importance = widget.MediumImportance
	v.token.Disable()
	buffer := &logBuffer{}
	v.buffer = buffer
	v.decoder = logview.Decoder{}
	v.clearLogs()
	v.stats = commandStats{}
	v.statistics.SetText(v.stats.text())
	v.latency = -1
	v.content.Refresh()
	run := client.RunWithCallbacks
	if invitation {
		run = client.RunInvitationWithCallbacks
	}
	go func() {
		defer stop()
		hasConnected := false // Accessed only on the Fyne UI thread.
		err := run(ctx, value, buffer, client.Callbacks{
			Redeemed: func(token string) {
				if target, id, err := client.ConnectionAddress(token); err == nil {
					address = target
					if targetURL, err := url.Parse(target); err == nil {
						host = targetURL.Host
						if id != "" {
							host += "/" + id
						}
					}
				}
				fyne.Do(func() {
					if !v.closed && current == v.generation {
						v.invitation = ""
						v.token.SetText(token)
					}
				})
			},
			Connecting: func() {
				fyne.Do(func() {
					if v.closed || current != v.generation || ctx.Err() != nil {
						return
					}
					v.showConnecting(host, tr("connecting"))
				})
			},
			Reconnecting: func(event client.ReconnectEvent) {
				fmt.Fprintf(buffer, "\n%s %s\n", time.Now().Format("15:04:05"), event.Text(uiLanguage))
				fyne.Do(func() {
					if v.closed || current != v.generation || ctx.Err() != nil {
						return
					}
					v.showConnecting(host, fmt.Sprintf(tr("connection.retry.status"), int(event.Delay.Seconds())))
					v.flushLogs()
				})
			},
			Latency: func(d time.Duration) {
				fyne.Do(func() {
					if v.closed || current != v.generation || ctx.Err() != nil || !v.connected {
						return
					}
					v.setLatency(d)
				})
			},
			Connected: func() {
				_ = client.RememberServer(address)
				fyne.Do(func() {
					if v.closed || current != v.generation || ctx.Err() != nil {
						return
					}
					v.connected, v.connecting = true, false
					v.address.SetText(host)
					v.setLatency(-1)
					v.disconnectButton.Enable()
					v.inputRow.Hide()
					v.connectedRow.Show()
					v.emptyTitle.SetText(tr("no.commands.yet"))
					v.emptyDetail.Show()
					if hasConnected {
						fmt.Fprintf(buffer, "\n%s %s\n", time.Now().Format("15:04:05"), tr("connection.restored"))
					}
					hasConnected = true
					v.flushLogs()
					v.refreshTerminalTheme()
					v.content.Refresh()
				})
			},
			Output: buffer.output,
			Command: func(event client.CommandEvent) {
				buffer.command(event)
				fyne.Do(func() {
					if v.closed || current != v.generation {
						return
					}
					v.stats.apply(event)
					v.statistics.SetText(v.stats.text())
					v.historyHeader.Refresh()
					v.refreshTerminalTheme()
				})
			},
		})
		fyne.Do(func() {
			if v.closed || current != v.generation {
				return
			}
			wasConnected := v.connected
			v.connected, v.connecting = false, false
			v.latency = -1
			v.status.SetText(tr("disconnected"))
			v.cancel = nil
			v.connectButton.SetText(tr("connect"))
			v.connectButton.SetIcon(arrowIcon)
			v.connectButton.Importance = widget.HighImportance
			v.connectButton.Enable()
			v.token.Enable()
			v.connectedRow.Hide()
			v.inputRow.Show()
			if err != nil {
				v.showConnectionError(tr("connection.ended") + i18n.Localize(err, uiLanguage))
				fmt.Fprintf(buffer, "\n%s%v\n", tr("connection.ended"), i18n.Localize(err, uiLanguage))
			} else if wasConnected {
				fmt.Fprintln(buffer, "\n"+tr("disconnected"))
			}
			v.refreshTerminalTheme()
			v.flushLogs()
			v.content.Refresh()
		})
	}()
}

func (v *clientView) showConnecting(host, status string) {
	v.connected, v.connecting, v.latency = false, true, -1
	v.address.SetText(host)
	v.status.SetText(status)
	v.inputRow.Hide()
	v.connectedRow.Show()
	v.disconnectButton.Enable()
	v.refreshTerminalTheme()
	v.content.Refresh()
}

func (v *clientView) setLatency(d time.Duration) {
	v.latency = d
	status := tr("connected")
	if d >= 0 {
		status += fmt.Sprintf(" · %dms", max(1, d.Milliseconds()))
	}
	v.status.SetText(status)
	v.header.Refresh()
}

func (v *clientView) showConnectionError(message string) {
	v.connectionError.SetText(message)
	v.connectionError.Show()
	v.content.Refresh()
}

func (v *clientView) disconnect() {
	if v.cancel != nil {
		v.latency = -1
		v.status.SetText(tr("disconnecting"))
		if v.connecting {
			v.connectButton.SetText(tr("cancelling"))
			v.connectButton.Disable()
		}
		v.disconnectButton.Disable()
		v.header.Refresh()
		v.cancel()
	}
}

func (v *clientView) refreshTerminalTheme() {
	if v.terminalTheme.inactive != !v.connected {
		v.terminalTheme.inactive = !v.connected
		v.terminalPanel.Refresh()
	}

}

func (v *clientView) clearLogs() {
	v.buffer.clear()
	v.logText = ""
	v.logSpans = nil
	v.logColors.Segments = nil
	v.logColors.Refresh()
	v.logSelection.SetText("")
	v.scroll.Hide()
	v.emptyTitle.SetText(tr("no.commands.yet"))
	v.emptyDetail.Show()
	if !v.connected {
		v.emptyTitle.SetText(tr("waiting.for.connection"))
		v.emptyDetail.Hide()
	}
	v.empty.Show()
}

func (v *clientView) flushLogs() {
	spans := v.buffer.takeSpans()
	if len(spans) == 0 {
		return
	}
	for _, span := range spans {
		span.text = v.decoder.Text(span.text)
		if span.text == "" {
			continue
		}
		if len(v.logSpans) > 0 && v.logSpans[len(v.logSpans)-1].kind == span.kind {
			v.logSpans[len(v.logSpans)-1].text += span.text
		} else {
			v.logSpans = append(v.logSpans, span)
		}
	}
	// Bound the displayed history as well as the pending buffer.
	total := 0
	for _, span := range v.logSpans {
		total += len(span.text)
	}
	v.logSpans, total = trimLogSpans(v.logSpans, total)
	var text strings.Builder
	text.Grow(total)
	for _, span := range v.logSpans {
		text.WriteString(span.text)
	}
	v.logText = text.String()
	wasSelecting := v.logSelection.SelectedText() != ""
	v.renderLog()
	v.empty.Hide()
	v.scroll.Show()
	if !wasSelecting {
		v.scroll.ScrollToBottom()
	}
}

func (v *clientView) renderLog() {
	segments := make([]widget.RichTextSegment, 0, len(v.logSpans))
	for _, span := range v.logSpans {
		colorName := theme.ColorNameForeground
		switch span.kind {
		case "system":
			colorName = colourSystem
		case "command":
			colorName = colourCommand
		case "error":
			colorName = theme.ColorNameError
		}
		segments = append(segments, &widget.TextSegment{Text: span.text, Style: widget.RichTextStyle{ColorName: colorName, Inline: true, SizeName: theme.SizeNameText}})
	}
	v.logColors.Segments = segments
	v.logColors.Refresh()
	if strings.HasPrefix(v.logText, v.logSelection.Text) {
		// Refresh preserves the active selection when output is appended.
		v.logSelection.Text = v.logText
		v.logSelection.Refresh()
	} else {
		// Truncation changes offsets, so discard the old selection.
		v.logSelection.SetText(v.logText)
	}
	v.terminal.Refresh()
}

func (v *clientView) startUpdates() {
	v.ticker = time.NewTicker(150 * time.Millisecond)
	go func() {
		for {
			select {
			case <-v.done:
				return
			case <-v.ticker.C:
				fyne.Do(func() {
					if !v.closed {
						v.flushLogs()
					}
				})
			}
		}
	}()
}

func (v *clientView) close() {
	if v.closed {
		return
	}
	v.closed = true
	v.generation++
	if v.cancel != nil {
		v.cancel()
	}
	if v.ticker != nil {
		v.ticker.Stop()
	}
	close(v.done)
}
