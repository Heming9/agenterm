package client

import (
	"agenterm/internal/i18n"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type persistentTerminal struct {
	history           *record
	submitted         chan struct{}
	c                 *Manager
	p                 *process
	id                string
	mu                sync.Mutex
	active            *CommandEvent
	reserved          bool
	persistent        bool
	retentionTimer    *time.Timer
	retentionDeadline time.Time
	autoClosed        bool
	lastExitCode      int
	ready             chan struct{}
	readyOnce         sync.Once
	dir, marker       string
	closed            bool
}

func (t *persistentTerminal) serve(ctx context.Context, first ExecuteRequest) {
	defer t.c.workers.Done()
	defer t.p.releaseTerminalSlot()
	defer func() {
		t.p.stop()
		if t.dir != "" {
			_ = os.RemoveAll(t.dir)
		}
	}()
	var cmd *exec.Cmd
	var err error
	t.dir, err = os.MkdirTemp("", "agenterm-shell-")
	if err == nil {
		options := ExecOptions{}
		if first.Setup != nil {
			options = first.Setup.ExecOptions
		}
		cmd = persistentShellCommand(ctx, t.marker, options)
		configureCommand(cmd, true)
		var input io.WriteCloser
		var output io.ReadCloser
		input, output, err = startPersistentInteractive(cmd)
		if err == nil {
			t.p.setIO(input, output)
			if line := persistentInitLine(t.marker); line != "" {
				if err = t.p.write(line); err != nil {
					t.p.stop()
				}
			}
			frames := &promptFrames{prefix: []byte(t.marker), output: t.output, complete: t.prompt}
			writer := newOutputWriter(frames)
			_, _ = io.Copy(writer, output)
			_ = writer.Close()
			frames.flush()
			err = cmd.Wait()
		}
	}
	code, detail := processResult(cmd, err)
	t.mu.Lock()
	t.closed = true
	t.stopRetentionTimerLocked()
	canceled := ctx.Err() != nil
	if t.autoClosed {
		code, detail = t.lastExitCode, ""
	} else if canceled {
		code, detail = -1, i18n.Text(i18n.Terminal(), "execution.canceled")
	}
	t.finishLocked(code, detail, false)
	t.readyOnce.Do(func() { close(t.ready) })
	select {
	case <-t.submitted:
	default:
		close(t.submitted)
	}
	t.mu.Unlock()
	// Release before confirming termination so a replacement can start as soon
	// as termination is confirmed. Startup failures release their reservation too.
	t.p.stop()
	t.p.releaseTerminalSlot()
	// Remove this process and close its result atomically with new admissions.
	t.c.mu.Lock()
	if t.c.running[t.id] == t.p {
		delete(t.c.running, t.id)
	}
	t.history.close(code, detail)
	t.c.pruneHistoryLocked()
	t.c.mu.Unlock()
}

func (t *persistentTerminal) execute(m execution) {
	// Startup must complete before the first command; its prompt is not a
	// completion of the submitted command. Startup has its own bounded wait.
	select {
	case <-t.ready:
	case <-time.After(10 * time.Second):
		t.p.stop()
		return
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	event := t.c.commandStarted(m.commandID, m.request.Command)
	t.active = &event
	t.reserved = false
	line, err := persistentCommandLine(t.dir, m.request.Command)
	t.mu.Unlock()
	if err == nil {
		err = t.p.write(line)
	}
	if err != nil {
		t.p.stop()
	}
	t.mu.Lock()
	select {
	case <-t.submitted:
	default:
		close(t.submitted)
	}
	t.mu.Unlock()
}

func (t *persistentTerminal) waitSubmitted(ctx context.Context) error {
	t.mu.Lock()
	gate := t.submitted
	t.mu.Unlock()
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *persistentTerminal) prompt(code int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.readyOnce.Do(func() { close(t.ready) })
	t.finishLocked(code, "", true)
}

func (t *persistentTerminal) finishLocked(code int, detail string, notify bool) {
	if t.active == nil {
		return
	}
	event := *t.active
	// A delayed timer goroutine must not turn a command that exceeded the
	// deadline into a one-shot success. Check the clock under the same lock.
	if !t.retentionDeadline.IsZero() && !time.Now().Before(t.retentionDeadline) {
		t.retainLocked()
	}
	t.active = nil
	t.lastExitCode = code
	autoClose := !t.persistent && !t.closed
	if autoClose {
		t.closed, t.autoClosed = true, true
		t.stopRetentionTimerLocked()
	}
	t.c.commandFinished(event, code, detail)
	if notify {
		t.history.complete(event.ID, code, detail)
	}
	if autoClose {
		t.p.stop()
	}
}

func (t *persistentTerminal) output(data []byte) error {
	// Discard only startup chatter before the integration is ready.
	select {
	case <-t.ready:
	default:
		return nil
	}
	t.mu.Lock()
	id := ""
	if t.active != nil {
		id = t.active.ID
	}
	t.history.append(data)
	t.mu.Unlock()
	return t.c.forwardOutput(id, data)
}

// Prompt framing is incremental: a frame or UTF-8 character may span reads.
// Only our random prefix followed by an integer and the terminator is removed.
type promptFrames struct {
	prefix   []byte
	pending  []byte
	output   func([]byte) error
	complete func(int)
}

func (f *promptFrames) Write(data []byte) (int, error) {
	f.pending = append(f.pending, data...)
	for len(f.pending) > 0 {
		i := bytes.Index(f.pending, f.prefix)
		if i < 0 {
			n := len(f.pending)
			for k := min(n, len(f.prefix)-1); k > 0; k-- {
				if bytes.Equal(f.pending[n-k:], f.prefix[:k]) {
					n -= k
					break
				}
			}
			if n > 0 {
				if err := f.output(f.pending[:n]); err != nil {
					return 0, err
				}
				f.pending = f.pending[n:]
			}
			break
		}
		if i > 0 {
			if err := f.output(f.pending[:i]); err != nil {
				return 0, err
			}
			f.pending = f.pending[i:]
		}
		end := bytes.IndexByte(f.pending[len(f.prefix):], '\x1f')
		if end < 0 {
			if len(f.pending) <= len(f.prefix)+16 {
				break
			}
			if err := f.output(f.pending[:1]); err != nil {
				return 0, err
			}
			f.pending = f.pending[1:]
			continue
		}
		end += len(f.prefix)
		code, err := strconv.Atoi(string(f.pending[len(f.prefix):end]))
		if err != nil {
			if err := f.output(f.pending[:1]); err != nil {
				return 0, err
			}
			f.pending = f.pending[1:]
			continue
		}
		f.pending = f.pending[end+1:]
		f.complete(code)
	}
	return len(data), nil
}

func (f *promptFrames) flush() {
	if len(f.pending) > 0 {
		_ = f.output(f.pending)
		f.pending = nil
	}
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func writeCommandScript(dir, command, extension string) (string, error) {
	// The script is sourced in the shell, preserving cd/export/functions. Sending
	// a single source line prevents multiline commands from consuming queued
	// protocol text as interactive program input.
	path := filepath.Join(dir, "command"+extension)
	return path, os.WriteFile(path, []byte(command+"\n"), 0600)
}
