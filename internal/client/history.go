package client

import (
	"agenterm/internal/i18n"
	"context"
	"sync"
	"time"
	"unicode/utf8"
)

const maxSessionOutput = 1 << 20

type record struct {
	mu                                        sync.Mutex
	id, commandID, command                    string
	started, ended                            time.Time
	output                                    []byte
	offset                                    int64
	changed                                   chan struct{}
	persistent, commandDone, closed, stopping bool
	exitCode, shellExitCode                   int
	err, shellError                           string
}

func newRecord(id string) *record {
	return &record{id: id, started: time.Now(), changed: make(chan struct{})}
}
func (r *record) signal() { close(r.changed); r.changed = make(chan struct{}) }
func (r *record) begin(id, command string, persistent bool) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commandID, r.command, r.persistent = id, command, persistent
	r.commandDone, r.stopping = false, false
	r.exitCode, r.err = 0, ""
	r.signal()
	return r.offset + int64(len(r.output))
}
func (r *record) append(data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.output = append(r.output, data...)
	if r.persistent {
		r.trim()
	}
	r.signal()
}
func (r *record) trim() {
	if len(r.output) <= maxSessionOutput {
		return
	}
	n := len(r.output) - maxSessionOutput
	for n < len(r.output) && !utf8.RuneStart(r.output[n]) {
		n++
	}
	r.output = append([]byte(nil), r.output[n:]...)
	r.offset += int64(n)
}
func (r *record) retain() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.persistent = true
	r.trim()
	r.signal()
}
func (r *record) complete(id string, code int, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.commandID != id || r.commandDone {
		return
	}
	r.commandDone, r.exitCode, r.err = true, code, detail
	r.signal()
}
func (r *record) close(code int, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	// Shell termination must not overwrite an already confirmed command result.
	if !r.commandDone {
		r.commandDone, r.exitCode, r.err = true, code, detail
	}
	r.closed, r.shellExitCode, r.shellError, r.ended = true, code, detail, time.Now()
	r.signal()
}
func (r *record) stopRequested() { r.mu.Lock(); defer r.mu.Unlock(); r.stopping = true; r.signal() }
func (r *record) status() string {
	if r.closed {
		return "closed"
	}
	if r.commandDone && r.persistent && !r.stopping {
		return "idle"
	}
	return "running"
}
func (r *record) info() (SessionInfo, time.Time, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := SessionInfo{SessionID: r.id, Command: r.command, CommandID: r.commandID, Status: r.status()}
	out.Summarize()
	if r.commandDone {
		out.ExitCode = new(r.exitCode)
		out.Error = r.err
	}
	return out, r.started, r.ended
}
func (r *record) read(ctx context.Context, a ReadRequest) (ReadOutput, error) {
	limit := DefaultReadBytes
	if a.MaxBytes != nil {
		limit = *a.MaxBytes
	}
	if limit < MinReadBytes || limit > MaxReadBytes {
		return ReadOutput{}, i18n.NewError("session.read.invalid.limit")
	}
	if a.Cursor < 0 || a.WaitMS < 0 || a.WaitMS > MaxWaitMS {
		return ReadOutput{}, i18n.NewError("session.read.invalid.arguments")
	}
	timer := time.NewTimer(time.Duration(a.WaitMS) * time.Millisecond)
	defer timer.Stop()
	cursor, wait := a.Cursor, a.WaitMS
	for {
		if err := ctx.Err(); err != nil {
			return ReadOutput{}, err
		}
		r.mu.Lock()
		if a.CommandID != "" && a.CommandID != r.commandID {
			r.mu.Unlock()
			return ReadOutput{}, i18n.NewError("session.command.changed")
		}
		end := r.offset + int64(len(r.output))
		if cursor > end {
			r.mu.Unlock()
			return ReadOutput{}, i18n.NewError("cursor.is.beyond.available.output")
		}
		truncated := cursor < r.offset
		if truncated {
			cursor = r.offset
		}
		if cursor < end || r.closed || r.commandDone || wait == 0 {
			if cursor < end && !utf8.RuneStart(r.output[cursor-r.offset]) {
				r.mu.Unlock()
				return ReadOutput{}, i18n.NewError("session.cursor.invalid.boundary")
			}
			next := min(end, cursor+int64(limit))
			for next < end && !utf8.RuneStart(r.output[next-r.offset]) {
				next--
			}
			out := ReadOutput{SessionID: r.id, CommandID: r.commandID, Status: r.status(), Output: string(r.output[cursor-r.offset : next-r.offset]), NextCursor: next, HasMore: next < end, Truncated: truncated}
			if r.commandDone {
				out.ExitCode = new(r.exitCode)
				out.Error = r.err
			}
			r.mu.Unlock()
			return out, nil
		}
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return ReadOutput{}, ctx.Err()
		case <-timer.C:
			wait = 0
		case <-changed:
		}
	}
}
func (r *record) wait(ctx context.Context, commandID string, cursor int64, anonymous bool, wait time.Duration) (Output, error) {
	timer := time.NewTimer(max(0, wait))
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return Output{}, err
		}
		r.mu.Lock()
		if commandID != r.commandID {
			r.mu.Unlock()
			return Output{}, i18n.NewError("session.command.changed")
		}
		finished, changed := r.commandDone || r.closed, r.changed
		if anonymous {
			finished = r.closed || r.persistent
		}
		r.mu.Unlock()
		if finished || (!anonymous && wait <= 0) {
			break
		}
		select {
		case <-ctx.Done():
			return Output{}, ctx.Err()
		case <-timer.C:
			wait = 0
		case <-changed:
		}
	}
	out, err := r.read(ctx, ReadRequest{CommandID: commandID, Cursor: cursor})
	if err != nil {
		return Output{}, err
	}
	r.mu.Lock()
	oneShot := anonymous && !r.persistent
	r.mu.Unlock()
	result := Output(out)
	if oneShot && !result.HasMore && !result.Truncated {
		result.SessionID = ""
	}
	return result, nil
}
func (r *record) waitStopped(ctx context.Context, wait time.Duration) (ControlResult, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		r.mu.Lock()
		closed, changed := r.closed, r.changed
		out := ControlResult{Status: "running"}
		if closed {
			out.Status = "closed"
			out.ExitCode = new(r.shellExitCode)
			out.Error = r.shellError
		}
		r.mu.Unlock()
		if closed || wait <= 0 {
			return out, nil
		}
		select {
		case <-ctx.Done():
			return ControlResult{}, ctx.Err()
		case <-timer.C:
			wait = 0
		case <-changed:
		}
	}
}
