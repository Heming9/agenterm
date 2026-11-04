package terminalhistory

import (
	"agenterm/internal/i18n"
	"context"
	"sync"
	"time"
	"unicode/utf8"

	"agenterm/internal/protocol"
)

const maxSessionOutput = 1 << 20
const defaultSessionReadBytes = 16 << 10
const maxSessionReadBytes = 64 << 10

type Record struct {
	id            string
	mu            sync.Mutex
	output        []byte
	offset        int64
	done          bool
	exitCode      int
	err           string
	timedOut      bool
	canceled      bool
	ended         time.Time
	changed       chan struct{}
	keepAlive     bool
	promoted      bool
	commandDone   bool
	commandID     string
	stopRequested bool
	command       string
	started       time.Time
}

func (p *Record) signal() { close(p.changed); p.changed = make(chan struct{}) }
func (p *Record) Accept(m protocol.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return
	}
	switch m.Type {
	case "output":
		p.output = append(p.output, []byte(m.Output)...)
		if p.keepAlive {
			p.trimOutputLocked()
		}
		p.signal()
	case "session_retained":
		p.keepAlive, p.promoted = m.SessionPersistent, m.SessionPromoted
		if p.keepAlive {
			p.trimOutputLocked()
		}
		p.signal()
	case "command_done":
		if m.RequestID != p.commandID || p.commandDone {
			return
		}
		p.commandDone, p.exitCode, p.err = true, m.ExitCode, m.Error
		p.timedOut, p.canceled = m.TimedOut, m.Canceled
		p.signal()
	case "done":
		p.commandDone = true
		p.done = true
		p.exitCode = m.ExitCode
		p.err = m.Error
		p.timedOut, p.canceled = m.TimedOut, m.Canceled
		p.ended = time.Now().UTC()

		p.signal()
	}
}

// Retained terminals use a bounded history. Before an anonymous terminal's
// lifetime is settled, keep all output so a completed command can be paged.
func (p *Record) trimOutputLocked() {
	if len(p.output) <= maxSessionOutput {
		return
	}
	n := len(p.output) - maxSessionOutput
	for n < len(p.output) && !utf8.RuneStart(p.output[n]) {
		n++
	}
	p.output = append([]byte(nil), p.output[n:]...)
	p.offset += int64(n)
}

// Anonymous cleanup and pending stop remain busy; idle must permit reuse.
// Closed means unavailable, including a lost connection reported via error.
func (p *Record) readStatusLocked() string {
	if p.done {
		return "closed"
	}
	if p.commandDone && p.keepAlive && !p.stopRequested {
		return "idle"
	}
	return "running"
}

func (p *Record) readCommand(ctx context.Context, cursor int64, wait time.Duration, maxBytes int, commandID string) (map[string]any, error) {
	if maxBytes < 4 || maxBytes > maxSessionReadBytes {
		return nil, i18n.NewError("session.read.invalid.limit")
	}
	if cursor < 0 || wait < 0 || wait > 20*time.Second {
		return nil, i18n.NewError("session.read.invalid.arguments")
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		p.mu.Lock()
		if commandID != "" && p.commandID != commandID {
			p.mu.Unlock()
			return nil, i18n.NewError("session.command.changed")
		}
		end := p.offset + int64(len(p.output))
		if cursor > end {
			p.mu.Unlock()
			return nil, i18n.NewError("cursor.is.beyond.available.output")
		}
		truncated := cursor < p.offset
		if truncated {
			cursor = p.offset
		}
		if cursor < end || p.done || p.commandDone || wait == 0 {
			if cursor < end && !utf8.RuneStart(p.output[cursor-p.offset]) {
				p.mu.Unlock()
				return nil, i18n.NewError("session.cursor.invalid.boundary")
			}
			next := min(end, cursor+int64(maxBytes))
			for next < end && !utf8.RuneStart(p.output[next-p.offset]) {
				next--
			}
			text := string(p.output[cursor-p.offset : next-p.offset])
			result := map[string]any{"session_id": p.id, "command_id": p.commandID, "status": p.readStatusLocked(), "output": text, "next_cursor": next, "has_more": next < end}
			if truncated {
				result["truncated"] = true
			}
			if p.done || p.commandDone {
				result["exit_code"] = p.exitCode
				if p.err != "" {
					result["error"] = p.err
				}
			}

			p.mu.Unlock()
			return result, nil
		}
		changed := p.changed
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			wait = 0
		case <-changed:
		}
	}
}

func (p *Record) Wait(ctx context.Context, a protocol.SessionReadRequest) (map[string]any, error) {
	if a.WaitMS < 0 || a.WaitMS > 20000 {
		return nil, i18n.NewError("session.read.invalid.arguments")
	}
	timer := time.NewTimer(time.Duration(a.WaitMS) * time.Millisecond)
	defer timer.Stop()
	for {
		p.mu.Lock()
		if p.commandID != a.CommandID {
			p.mu.Unlock()
			return nil, i18n.NewError("session.command.changed")
		}
		finished, changed := p.commandDone || p.done, p.changed
		if a.AwaitSettlement {
			finished = p.done || p.keepAlive
		}
		p.mu.Unlock()
		if finished || (!a.AwaitSettlement && a.WaitMS == 0) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			a.WaitMS = 0
			// Anonymous lifetime is decided and acknowledged by the client.
			// Its retention event or actual shell exit settles this waiter.
		case <-changed:
		}
	}
	// A completed anonymous command needs an ID only when the first page
	// leaves output to read or the requested history was truncated.
	p.mu.Lock()
	oneShot := a.AwaitSettlement && !p.keepAlive
	p.mu.Unlock()
	result, err := p.readCommand(ctx, a.Cursor, 0, defaultSessionReadBytes, a.CommandID)
	if err == nil && oneShot && result["has_more"] == false && result["truncated"] != true {
		delete(result, "session_id")
	}
	return result, err
}

func New(id string) *Record {
	return &Record{id: id, changed: make(chan struct{}), started: time.Now()}
}

// Begin records the boundary before admitting a new command.
func (p *Record) Begin(commandID, command string, persistent bool) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.commandID, p.command = commandID, command
	p.keepAlive = persistent
	p.commandDone, p.done, p.stopRequested = false, false, false
	p.exitCode, p.err = 0, ""
	p.signal()
	return p.offset + int64(len(p.output))
}
func (p *Record) Read(ctx context.Context, a protocol.SessionReadRequest) (map[string]any, error) {
	limit := a.MaxBytes
	if limit == 0 {
		limit = defaultSessionReadBytes
	}
	return p.readCommand(ctx, a.Cursor, time.Duration(a.WaitMS)*time.Millisecond, limit, a.CommandID)
}
func (p *Record) StopRequested() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopRequested = true
	p.signal()
}
func (p *Record) Ended() time.Time { p.mu.Lock(); defer p.mu.Unlock(); return p.ended }
