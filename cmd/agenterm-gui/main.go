package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"agenterm/internal/client"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// logBuffer bounds memory while the GUI consumes output from multiple processes.
type logBuffer struct {
	mu   sync.Mutex
	data string
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data += string(p)
	if len(b.data) > 65536 {
		b.data = b.data[len(b.data)-65536:]
		for len(b.data) > 0 && !utf8.RuneStart(b.data[0]) {
			b.data = b.data[1:]
		}
	}
	return len(p), nil
}
func (b *logBuffer) take() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.data
	b.data = ""
	return s
}

var _ io.Writer = (*logBuffer)(nil)

func main() {
	a := app.NewWithID("io.agenterm.client")
	a.Settings().SetTheme(theme.DefaultTheme())
	win := a.NewWindow("Agenterm")
	win.Resize(fyne.NewSize(740, 570))

	title := widget.NewLabelWithStyle("Agenterm", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	title.TextStyle = fyne.TextStyle{Bold: true}
	title.Importance = widget.HighImportance
	subtitle := widget.NewLabel("让 Agent 连接并操作这台电脑")
	status := widget.NewLabelWithStyle("●  未连接", fyne.TextAlignTrailing, fyne.TextStyle{Bold: true})
	status.Importance = widget.MediumImportance
	token := widget.NewEntry()
	token.SetPlaceHolder("粘贴 Server 生成的连接 Token")
	hint := widget.NewLabel("连接后，Server 可以在当前用户权限内执行命令。")
	hint.Wrapping = fyne.TextWrapWord
	hint.Importance = widget.MediumImportance
	logLabel := widget.NewLabel("连接后，Agent 执行的命令和输出会显示在这里。")
	logLabel.Wrapping = fyne.TextWrapBreak
	scroll := container.NewVScroll(logLabel)
	scroll.SetMinSize(fyne.NewSize(0, 230))
	logs := &logBuffer{}
	var cancel context.CancelFunc
	var connected bool
	var generation uint64
	var connectButton, disconnectButton *widget.Button
	connectButton = widget.NewButton("连接", func() {
		value := strings.TrimSpace(token.Text)
		if value == "" {
			status.SetText("●  请输入 Token")
			status.Importance = widget.WarningImportance
			status.Refresh()
			return
		}
		generation++
		current := generation
		ctx, stop := context.WithCancel(context.Background())
		cancel = stop
		connected = false
		status.SetText("●  连接中")
		status.Importance = widget.MediumImportance
		status.Refresh()
		connectButton.Disable()
		disconnectButton.Enable()
		token.Disable()
		fmt.Fprintln(logs, "正在连接…")
		go func() {
			err := client.Run(ctx, value, logs, func() {
				fyne.Do(func() {
					if current != generation || ctx.Err() != nil {
						return
					}
					connected = true
					status.SetText("●  已连接")
					status.Importance = widget.SuccessImportance
					status.Refresh()
					token.SetText("")
					fmt.Fprintln(logs, "已连接，等待 Agent 执行命令。")
				})
			})
			fyne.Do(func() {
				if current != generation {
					return
				}
				wasConnected := connected
				connected = false
				cancel = nil
				connectButton.Enable()
				disconnectButton.Disable()
				token.Enable()
				if err != nil {
					status.SetText("●  连接失败")
					status.Importance = widget.DangerImportance
					fmt.Fprintf(logs, "连接已结束：%v\n", err)
				} else {
					status.SetText("●  未连接")
					status.Importance = widget.MediumImportance
					if wasConnected {
						fmt.Fprintln(logs, "连接已断开。")
					} else {
						fmt.Fprintln(logs, "连接已取消。")
					}
				}
				status.Refresh()
			})
		}()
	})
	connectButton.Importance = widget.HighImportance
	disconnectButton = widget.NewButton("断开", func() {
		if cancel != nil {
			status.SetText("●  正在断开")
			disconnectButton.Disable()
			cancel()
		}
	})
	disconnectButton.Disable()
	header := container.NewBorder(nil, nil, nil, status, container.NewVBox(title, subtitle))
	connection := container.NewVBox(widget.NewLabelWithStyle("连接设备", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), token, container.NewHBox(connectButton, disconnectButton), hint)
	historyHeader := container.NewBorder(nil, nil, nil, widget.NewButtonWithIcon("清空", theme.DeleteIcon(), func() { logLabel.SetText("") }), widget.NewLabelWithStyle("执行记录", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	body := container.NewBorder(container.NewVBox(header, widget.NewSeparator(), connection, widget.NewSeparator(), historyHeader), nil, nil, nil, scroll)
	win.SetContent(container.NewPadded(body))
	ticker := time.NewTicker(150 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				chunk := logs.take()
				if chunk == "" {
					continue
				}
				fyne.Do(func() {
					text := logLabel.Text
					if strings.Contains(text, "连接后，Agent 执行的命令") {
						text = ""
					}
					text += chunk
					if len(text) > 65536 {
						text = text[len(text)-65536:]
						for len(text) > 0 && !utf8.RuneStart(text[0]) {
							text = text[1:]
						}
					}
					logLabel.SetText(text)
					scroll.ScrollToBottom()
				})
			}
		}
	}()
	win.SetCloseIntercept(func() {
		if cancel != nil {
			cancel()
		}
		ticker.Stop()
		close(done)
		win.Close()
	})
	win.ShowAndRun()
}
