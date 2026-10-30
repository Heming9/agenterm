package main

import (
	"context"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"time"

	"agenterm/internal/client"
	"agenterm/internal/protocol"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type commandStats struct {
	completed int
	total     time.Duration
	running   map[string]bool
}

func (s *commandStats) apply(event client.CommandEvent) {
	if s.running == nil {
		s.running = make(map[string]bool)
	}
	if !event.Done {
		s.running[event.ID] = true
		return
	}
	delete(s.running, event.ID)
	s.completed++
	s.total += event.Duration
}

func (s *commandStats) text() string {
	average := "—"
	if s.completed > 0 {
		average = durationText(s.total / time.Duration(s.completed))
	}
	return fmt.Sprintf("已执行 %d 条  ·  平均耗时 %s", s.completed, average)
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
	terminal                        *widget.Label
	emptyTitle, emptyDetail         *uiText
	terminalTheme                   *monochromeTheme
	terminalPanel                   *container.ThemeOverride
	empty                           *fyne.Container
	scroll                          *container.Scroll

	buffer     *logBuffer
	logText    string
	decoder    displayDecoder
	stats      commandStats
	cancel     context.CancelFunc
	connected  bool
	connecting bool
	closed     bool
	generation uint64
	ticker     *time.Ticker
	done       chan struct{}
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
	v.token.SetPlaceHolder("粘贴 Server 生成的连接 Token")
	v.token.OnSubmitted = func(string) { v.connect() }
	v.connectButton = newButton("连接", arrowIcon, func() {
		if v.connecting {
			v.disconnect()
		} else {
			v.connect()
		}
	})
	v.connectButton.IconPlacement = widget.ButtonIconTrailingText
	v.connectButton.Importance = widget.HighImportance
	v.disconnectButton = newButton("断开连接", unplugIcon, v.disconnect)
	v.address = mutedLabel("")
	v.address.TextStyle.Monospace = true
	v.status = newText("●  已连接", 12, colourTagText)
	v.statusBadge = newSurface(padded(v.status, 4, 4, 10, 10), 6)
	v.statusBadge.fill, v.statusBadge.border = colourTag, false
	connectControl := container.NewThemeOverride(v.connectButton, buttonTheme{newMonochromeTheme(false)})
	disconnectControl := container.NewThemeOverride(v.disconnectButton, buttonTheme{newMonochromeTheme(false)})
	tokenControl := container.NewThemeOverride(v.token, entryTheme{Theme: newMonochromeTheme(false), height: 38})
	v.inputRow = container.NewBorder(nil, nil, nil, padded(slot(connectControl, 84, 38, false), 0, 0, 8, 0), tokenControl)
	v.connectedRow = container.NewBorder(nil, nil, nil,
		container.New(layout.NewCustomPaddedHBoxLayout(14), v.address, container.NewCenter(v.statusBadge),
			slot(newSurface(disconnectControl, 7), 112, 38, false)), nil)
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
	clearButton := newButton("清空", trashIcon, v.clearLogs)
	clearButton.Importance = widget.LowImportance
	clearControl := container.NewThemeOverride(clearButton, compactControlTheme{newMonochromeTheme(false)})
	historyTitle := newText("执行记录", 14, theme.ColorNameForeground)
	v.historyHeader = container.NewBorder(nil, nil, historyTitle,
		container.New(layout.NewCustomPaddedHBoxLayout(12), v.statistics,
			container.NewCenter(slot(widget.NewSeparator(), 1, 16, false)), clearControl), nil)

	v.terminal = widget.NewLabel("")
	v.terminal.Selectable = true
	v.terminal.Wrapping = fyne.TextWrapBreak
	v.scroll = container.NewVScroll(padded(v.terminal, 20, 20, 20, 20))
	v.scroll.SetMinSize(fyne.NewSize(0, 180))
	v.scroll.Hide()
	v.emptyTitle = mutedLabel("等待连接")
	v.emptyTitle.Alignment = fyne.TextAlignCenter
	v.emptyDetail = mutedLabel("等待 Agent 执行下一个命令。")
	v.emptyDetail.Alignment = fyne.TextAlignCenter
	v.emptyDetail.Hide()
	v.empty = container.NewCenter(container.New(layout.NewCustomPaddedVBoxLayout(7),
		container.NewCenter(slot(widget.NewIcon(theme.NewColoredResource(terminalIcon, theme.ColorNamePlaceHolder)), 27, 27, false)),
		v.emptyTitle, v.emptyDetail))
	viewport := container.NewStack(v.scroll, v.empty)
	shell := "/bin/sh"
	if runtime.GOOS == "windows" {
		shell = "cmd.exe"
	}
	toolbarTitle := newText("命令与输出", 11, theme.ColorNamePlaceHolder)
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
	decoded, err := protocol.Decode(value)
	if err != nil {
		message := "Token 格式不正确，请粘贴 Server 生成的完整 Token。"
		if value == "" {
			message = "请先输入连接 Token。"
		}
		v.showConnectionError(message)
		v.window.Canvas().Focus(v.token)
		return
	}
	serverURL, _ := url.Parse(decoded.URL)
	// Show only the host, never the secret or any URL credentials/query string.
	host := serverURL.Host
	v.generation++
	current := v.generation
	ctx, stop := context.WithCancel(context.Background())
	v.cancel = stop
	v.connecting = true
	v.connectionError.Hide()
	v.connectButton.SetText("取消连接")
	v.connectButton.SetIcon(theme.CancelIcon())
	v.connectButton.Importance = widget.MediumImportance
	v.token.Disable()
	buffer := &logBuffer{}
	v.buffer = buffer
	v.decoder = displayDecoder{}
	v.content.Refresh()
	go func() {
		defer stop()
		err := client.RunWithCallbacks(ctx, value, buffer, client.Callbacks{
			Connected: func() {
				fyne.Do(func() {
					if v.closed || current != v.generation || ctx.Err() != nil {
						return
					}
					v.connected, v.connecting = true, false
					v.stats = commandStats{}
					v.statistics.SetText(v.stats.text())
					v.address.SetText(host)
					v.status.SetText("●  已连接")
					v.disconnectButton.Enable()
					v.token.SetText("")
					v.inputRow.Hide()
					v.connectedRow.Show()
					v.logText = ""
					v.emptyTitle.SetText("暂无执行记录")
					v.emptyDetail.Show()
					v.empty.Show()
					v.scroll.Hide()
					v.refreshTerminalTheme()
					v.content.Refresh()
				})
			},
			Command: func(event client.CommandEvent) {
				writeCommandEvent(buffer, event)
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
			v.cancel = nil
			v.connectButton.SetText("连接")
			v.connectButton.SetIcon(arrowIcon)
			v.connectButton.Importance = widget.HighImportance
			v.connectButton.Enable()
			v.token.Enable()
			v.connectedRow.Hide()
			v.inputRow.Show()
			if err != nil {
				v.showConnectionError("连接已结束：" + err.Error())
				fmt.Fprintf(buffer, "\n连接已结束：%v\n", err)
			} else if wasConnected {
				fmt.Fprintln(buffer, "\n连接已断开。")
			}
			v.refreshTerminalTheme()
			v.flushLogs()
			v.content.Refresh()
		})
	}()
}

func writeCommandEvent(buffer *logBuffer, event client.CommandEvent) {
	if !event.Done {
		fmt.Fprintf(buffer, "\n%s  $ %s\n", event.StartedAt.Format("15:04:05"), event.Command)
		return
	}
	if event.Error != "" {
		fmt.Fprintf(buffer, "\n%s\n", event.Error)
	}
}

func (v *clientView) showConnectionError(message string) {
	v.connectionError.SetText(message)
	v.connectionError.Show()
	v.content.Refresh()
}

func (v *clientView) disconnect() {
	if v.cancel != nil {
		v.status.SetText("●  正在断开")
		if v.connecting {
			v.connectButton.SetText("正在取消…")
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
	v.buffer.take()
	v.logText = ""
	v.terminal.SetText("")
	v.scroll.Hide()
	v.emptyTitle.SetText("暂无执行记录")
	v.emptyDetail.Show()
	if !v.connected {
		v.emptyTitle.SetText("等待连接")
		v.emptyDetail.Hide()
	}
	v.empty.Show()
}

func (v *clientView) flushLogs() {
	chunk := v.buffer.take()
	if chunk == "" {
		return
	}
	v.logText = logTail(v.logText + v.decoder.text(chunk))
	v.terminal.SetText(strings.Trim(v.logText, "\n"))
	v.empty.Hide()
	v.scroll.Show()
	v.scroll.ScrollToBottom()
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
