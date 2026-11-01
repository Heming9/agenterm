package main

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	osc52 "github.com/aymanbagabas/go-osc52/v2"
	tea "github.com/charmbracelet/bubbletea"
)

type tuiClipboard struct {
	mu      sync.Mutex
	request atomic.Uint64
	write   func(context.Context, string) (string, error)
}

type tuiCopyResult struct {
	selectionID, request uint64
	notice               string
	err                  error
}

func (m *tuiModel) copySelection() tea.Cmd {
	text := m.selectedText()
	if text == "" {
		return nil
	}
	clipboard := m.clipboard
	request, id := clipboard.request.Add(1), m.selectionID
	return func() tea.Msg {
		// Serialize writes, dropping stale requests so the latest selection wins.
		clipboard.mu.Lock()
		defer clipboard.mu.Unlock()
		if request != clipboard.request.Load() {
			return nil
		}
		ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
		defer cancel()
		notice, err := clipboard.write(ctx, text)
		return tuiCopyResult{selectionID: id, request: request, notice: notice, err: err}
	}
}

func writeTUIClipboard(ctx context.Context, text string) (string, error) {
	var args []string
	if os.Getenv("SSH_CONNECTION") == "" && os.Getenv("SSH_TTY") == "" {
		switch runtime.GOOS {
		case "darwin":
			args = []string{"pbcopy"}
		case "windows":
			args = []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "[Console]::InputEncoding = [System.Text.UTF8Encoding]::new(); Set-Clipboard -Value ([Console]::In.ReadToEnd())"}
		default:
			if os.Getenv("WAYLAND_DISPLAY") != "" {
				if _, err := exec.LookPath("wl-copy"); err == nil {
					args = []string{"wl-copy"}
				}
			}
			if len(args) == 0 && os.Getenv("DISPLAY") != "" {
				if _, err := exec.LookPath("xclip"); err == nil {
					args = []string{"xclip", "-selection", "clipboard"}
				} else if _, err := exec.LookPath("xsel"); err == nil {
					args = []string{"xsel", "--clipboard", "--input"}
				}
			}
		}
	}
	if len(args) > 0 {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Stdin = strings.NewReader(text)
		return "tui.selection.copied", cmd.Run()
	}
	// Non-printing OSC 52 does not change the renderer's screen contents.
	// Remote terminals may require clipboard access to be enabled explicitly.
	sequence := osc52.New(text)
	if os.Getenv("TMUX") != "" {
		sequence = sequence.Tmux()
	} else if strings.HasPrefix(os.Getenv("TERM"), "screen") {
		sequence = sequence.Screen()
	}
	_, err := sequence.WriteTo(os.Stdout)
	return "tui.selection.sent", err
}
