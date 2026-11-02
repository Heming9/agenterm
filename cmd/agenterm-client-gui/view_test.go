package main

import (
	"bytes"
	"context"
	"fmt"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"agenterm/internal/client"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"github.com/go-text/typesetting/font"
)

func TestConnectionLatencyAndReconnectStatus(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(newMonochromeTheme(false))
	w := a.NewWindow("Agenterm")
	defer w.Close()
	v := newClientView(w)
	defer v.close()
	w.SetPadded(false)
	w.SetContent(v.content)
	w.Resize(fyne.NewSize(700, 420))
	v.connected = true
	v.address.SetText("agenterm.net")
	v.inputRow.Hide()
	v.connectedRow.Show()
	v.setLatency(42 * time.Millisecond)
	fmt.Fprint(v.buffer, "connection history\n")
	v.flushLogs()
	if !strings.HasSuffix(v.status.Text, "已连接 · 42ms") {
		t.Fatal(v.status.Text)
	}
	capture := func(name string) {
		if path := os.Getenv("AGENTERM_SCREENSHOTS"); path != "" {
			if err := os.MkdirAll(path, 0755); err != nil {
				t.Fatal(err)
			}
			f, err := os.Create(filepath.Join(path, name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, w.Canvas().Capture()); err != nil {
				t.Fatal(err)
			}
		}
	}
	capture("client-latency")
	v.showConnecting("agenterm.net", fmt.Sprintf(tr("connection.retry.status"), 30))
	if v.connected || !v.connecting || v.latency >= 0 || strings.Contains(v.status.Text, "ms") || !strings.Contains(v.logText, "connection history") {
		t.Fatal("bad reconnect state")
	}
	capture("client-reconnecting")
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	test.Tap(v.disconnectButton)
	if ctx.Err() == nil || v.latency >= 0 {
		t.Fatal("cancel did not stop reconnection")
	}
}

func TestInterfaceFontCoversChineseWithoutFallback(t *testing.T) {
	if systemSans == nil {
		t.Skip("no installed standalone CJK font override on this system")
	}
	face, err := font.ParseTTF(bytes.NewReader(systemSans.Content()))
	if err != nil {
		t.Fatal(err)
	}
	const labels = "Agenterm 粘贴 Server 生成的连接 Token 已执行 0 条 平均耗时 — 执行记录 命令与输出 等待连接 后，命令和输出将显示在这里。断开连接 清空 正在执行"
	for _, r := range labels {
		if _, ok := face.NominalGlyph(r); !ok {
			t.Errorf("UI character %q would require a different fallback font", r)
		}
	}
}

type fixedVariantTheme struct {
	fyne.Theme
	variant fyne.ThemeVariant
}

func (t fixedVariantTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	return t.Theme.Color(name, t.variant)
}

func TestCommandStatisticsAndClear(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(newMonochromeTheme(false))
	win := a.NewWindow("Agenterm")
	win.SetPadded(false)
	defer win.Close()
	v := newClientView(win)
	defer v.close()
	win.SetContent(v.content)
	win.Resize(fyne.NewSize(960, 640))

	// Concurrent commands, failed exits, and cancellations all contribute once
	// they finish; a running command never enters the average prematurely.
	v.stats.apply(client.CommandEvent{ID: "a"})
	v.stats.apply(client.CommandEvent{ID: "b"})
	if v.stats.text() != "已执行 0 条  ·  平均耗时 —" {
		t.Fatal(v.stats.text())
	}
	v.stats.apply(client.CommandEvent{ID: "a", Done: true, Duration: 200 * time.Millisecond})
	v.stats.apply(client.CommandEvent{ID: "b", Done: true, Duration: 800 * time.Millisecond, ExitCode: 7})
	v.stats.apply(client.CommandEvent{ID: "c"})
	if v.stats.text() != "已执行 2 条  ·  平均耗时 500 ms" || len(v.stats.running) != 1 {
		t.Fatalf("unexpected stats: %s running=%v", v.stats.text(), v.stats.running)
	}
	v.statistics.SetText(v.stats.text())
	fmt.Fprint(v.buffer, "test output\n")
	v.flushLogs()
	if v.logText == "" || !v.scroll.Visible() {
		t.Fatal("output did not reach the terminal")
	}
	v.clearLogs()
	if v.logText != "" || v.scroll.Visible() || !v.empty.Visible() || v.stats.completed != 2 {
		t.Fatal("clearing logs changed statistics or left output visible")
	}
	test.Tap(v.connectButton) // Reject an empty token through the actual styled control.
	if !v.connectionError.Visible() || v.connecting || v.cancel != nil {
		t.Fatal("invalid token started a connection")
	}
	v.connectionError.Hide()

	// Optional screenshots are generated from the real Fyne view for visual QA.
	if path := os.Getenv("AGENTERM_SCREENSHOTS"); path != "" {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
		capture := func(name string) {
			t.Helper()
			v.content.Refresh()
			f, err := os.Create(filepath.Join(path, name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, win.Canvas().Capture()); err != nil {
				t.Fatal(err)
			}
		}
		capture("client-disconnected")
		v.showConnectionError("Token 格式不正确，请粘贴 Server 生成的完整 Token。")
		capture("client-error")
		v.connectionError.Hide()
		test.Type(v.token, "agenterm:preview")
		capture("client-entry-filled")
		v.token.SetText("")
		win.Canvas().Focus(nil)
		v.connected = true
		v.address.SetText("192.168.1.8:8080")
		v.inputRow.Hide()
		v.connectedRow.Show()
		v.refreshTerminalTheme()
		fmt.Fprint(v.buffer, "14:32:15 $ pwd\n/Users/ming/Projects/agenterm\n\n14:32:18 $ go test ./internal/client\nok  agenterm/internal/client  0.800s\n")
		v.flushLogs()
		capture("client-connected")
		win.Resize(fyne.NewSize(700, 420))
		capture("client-compact")
		a.Settings().SetTheme(fixedVariantTheme{Theme: newMonochromeTheme(false), variant: theme.VariantDark})
		win.Resize(fyne.NewSize(960, 640))
		capture("client-dark")
		v.showConnectionError("Token 格式不正确，请粘贴 Server 生成的完整 Token。")
		capture("client-error-dark")
		v.connectionError.Hide()
	}
}

func TestLogDecodingAcrossChunks(t *testing.T) {
	var decoder displayDecoder
	chunks := []string{"hello \x1b[3", "2m", string([]byte{0xe4, 0xb8}), string([]byte{0xad}), "\x1b[0m\r\n", "\x1b]0;hidden title\x1b", "\\world"}
	var got string
	for _, chunk := range chunks {
		got += decoder.text(chunk)
	}
	if got != "hello 中\nworld" {
		t.Fatalf("bad decoded output: %q", got)
	}
	buffer := &logBuffer{}
	fmt.Fprint(buffer, strings.Repeat("中", maxLogBytes))
	retained := buffer.take()
	if len(retained) > maxLogBytes || !utf8.ValidString(retained) || buffer.take() != "" {
		t.Fatal("output buffer is unbounded or splits UTF-8")
	}
}

func TestMonochromeTheme(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	for _, terminal := range []bool{false, true} {
		th := newMonochromeTheme(terminal)
		for _, variant := range []fyne.ThemeVariant{theme.VariantLight, theme.VariantDark} {
			for _, name := range []fyne.ThemeColorName{theme.ColorNameBackground, theme.ColorNameForeground,
				theme.ColorNamePrimary, theme.ColorNameSuccess, theme.ColorNameWarning,
				theme.ColorNameButton, theme.ColorNameInputBorder, theme.ColorNamePlaceHolder, theme.ColorNameFocus} {
				r, g, b, _ := th.Color(name, variant).RGBA()
				if r != g || g != b {
					t.Fatalf("non-neutral color %s in variant %d", name, variant)
				}
			}
		}
	}
}
