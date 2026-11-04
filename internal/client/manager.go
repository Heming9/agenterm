package client

import (
	"agenterm/internal/i18n"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type CommandEvent struct {
	ID, Command string
	Interactive bool
	StartedAt   time.Time
	Done        bool
	Duration    time.Duration
	ExitCode    int
	Error       string
}

// TerminalCallbacks receive terminal events outside manager and process locks.
type TerminalCallbacks struct {
	Command func(CommandEvent)
	Output  func(string, []byte)
}
type Manager struct {
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	closed        bool
	running       map[string]*process
	history       map[string]*record
	workers       sync.WaitGroup
	log           io.Writer
	callbacks     TerminalCallbacks
	eventsMu      sync.Mutex
	events        []func()
	eventsWake    chan struct{}
	eventsClosed  bool
	eventsDone    chan struct{}
	eventsBytes   int
	eventsChanged chan struct{}
}

var _ API = (*Manager)(nil)

func NewManager(log io.Writer, callbacks TerminalCallbacks) *Manager {
	if log == nil {
		log = io.Discard
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{ctx: ctx, cancel: cancel, log: log, callbacks: callbacks, running: make(map[string]*process), history: make(map[string]*record), eventsWake: make(chan struct{}, 1), eventsChanged: make(chan struct{}), eventsDone: make(chan struct{})}
	go m.deliverEvents()
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.mu.Lock()
				m.pruneHistoryLocked()
				m.mu.Unlock()
			}
		}
	}()
	return m
}
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	for _, p := range m.running {
		p.stop()
	}
	m.mu.Unlock()
	m.workers.Wait()
	m.mu.Lock()
	clear(m.history)
	clear(m.running)
	m.mu.Unlock()
	m.eventsMu.Lock()
	m.eventsClosed = true
	m.eventsMu.Unlock()
	select {
	case m.eventsWake <- struct{}{}:
	default:
	}
}

// WaitCallbacks drains observer notifications after Close. Keep this wait outside
// callbacks, which may themselves call Close without waiting on their dispatcher.
func (m *Manager) WaitCallbacks() { <-m.eventsDone }
func Controls() []string          { return interactiveControls() }
func randomID(bytes int) (string, error) {
	b := make([]byte, bytes)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}

type execution struct {
	request   ExecuteRequest
	commandID string
	record    *record
	cursor    int64
	anonymous bool
	deadline  time.Time
}

func (m *Manager) Execute(ctx context.Context, a ExecuteRequest) (Output, error) {
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}
	if a.Setup != nil {
		setup := *a.Setup
		setup.Env = maps.Clone(a.Setup.Env)
		a.Setup = &setup
	}
	if strings.TrimSpace(a.Command) == "" {
		return Output{}, i18n.NewError("command.is.required")
	}
	if err := ValidateSessionRequest(a.SessionID, a.Setup); err != nil {
		return Output{}, err
	}
	wait := DefaultWaitMS
	if a.WaitMS != nil {
		wait = *a.WaitMS
	}
	if wait < 0 || wait > MaxWaitMS {
		return Output{}, i18n.NewError("session.read.invalid.arguments")
	}
	e, err := m.admit(a, wait)
	if err != nil {
		return Output{}, err
	}
	return e.record.wait(ctx, e.commandID, e.cursor, e.anonymous, time.Until(e.deadline))
}
func (m *Manager) admit(a ExecuteRequest, wait int) (execution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return execution{}, i18n.NewError("session.already.ended")
	}
	m.pruneHistoryLocked()
	id := a.SessionID
	if a.Setup != nil {
		id = a.Setup.SessionID
	}
	p := m.running[id]
	if a.Setup != nil && p != nil {
		return execution{}, i18n.NewError("session.already.exists", id)
	}
	if a.SessionID != "" && p == nil {
		return execution{}, i18n.NewError("unknown.session")
	}
	commandID, err := randomID(16)
	if err != nil {
		return execution{}, err
	}
	e := execution{request: a, commandID: commandID, deadline: time.Now().Add(time.Duration(wait) * time.Millisecond)}
	created := p == nil
	var startCtx context.Context
	if created {
		if !acquireTerminalSlot() {
			return execution{}, i18n.NewError("session.limit.reached.try.again.later")
		}
		ctx, cancel := context.WithCancel(m.ctx)
		startCtx = ctx
		p = &process{cancel: cancel, ready: make(chan struct{}), inputQueue: make(chan inputRequest, 32), done: ctx.Done()}
		marker, err := randomID(24)
		if err != nil {
			cancel()
			p.releaseTerminalSlot()
			return execution{}, err
		}
		for id == "" {
			candidate, err := randomID(3)
			if err != nil {
				cancel()
				p.releaseTerminalSlot()
				return execution{}, err
			}
			if m.history[candidate] == nil {
				id = candidate
			}
		}
		persistent := (a.Setup != nil && a.Setup.SessionID != "") || wait == 0
		t := &persistentTerminal{c: m, p: p, id: id, history: newRecord(id), ready: make(chan struct{}), marker: "\x1eagenterm:" + marker + ":", submitted: make(chan struct{}), reserved: true, persistent: persistent}
		p.terminal = t
		m.running[id] = p
		m.history[id] = t.history
	} else {
		t := p.terminal
		t.mu.Lock()
		if t.closed {
			err = i18n.NewError("session.already.ended")
		} else if !t.persistent || t.reserved || t.active != nil {
			err = i18n.NewError("session.busy", id)
		}
		if err != nil {
			t.mu.Unlock()
			return execution{}, err
		}
		t.reserved = true
		t.submitted = make(chan struct{})
		t.mu.Unlock()
	}
	t := p.terminal
	t.mu.Lock()
	e.record = t.history
	e.anonymous = !t.persistent
	e.cursor = t.history.begin(commandID, a.Command, t.persistent)
	if created && !t.persistent {
		t.retentionDeadline = e.deadline
		t.retentionTimer = time.AfterFunc(max(0, time.Until(e.deadline)), func() { t.retain() })
	}
	t.mu.Unlock()
	// All worker reservations are made before releasing the manager lock, so
	// Close cannot return while a newly admitted process is still starting.
	if created {
		m.workers.Add(2)
		go m.inputWorker(startCtx, p)
		go t.serve(startCtx, a)
	}
	m.workers.Add(1)
	go func() { defer m.workers.Done(); t.execute(e) }()
	return e, nil
}
func (m *Manager) lookup(id string) (*record, *process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneHistoryLocked()
	if m.closed {
		return nil, nil, i18n.NewError("session.already.ended")
	}
	r := m.history[id]
	if id == "" || r == nil {
		return nil, nil, i18n.NewError("unknown.session")
	}
	return r, m.running[id], nil
}
func (m *Manager) Read(ctx context.Context, a ReadRequest) (ReadOutput, error) {
	r, _, err := m.lookup(a.SessionID)
	if err != nil {
		return ReadOutput{}, err
	}
	return r.read(ctx, a)
}
func (m *Manager) List(ctx context.Context) (ListResult, error) {
	if err := ctx.Err(); err != nil {
		return ListResult{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ListResult{}, i18n.NewError("session.already.ended")
	}
	m.pruneHistoryLocked()
	type item struct {
		info    SessionInfo
		started time.Time
	}
	items := make([]item, 0, len(m.history))
	for _, r := range m.history {
		info, started, _ := r.info()
		items = append(items, item{info, started})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].started.Equal(items[j].started) {
			return items[i].info.SessionID < items[j].info.SessionID
		}
		return items[i].started.Before(items[j].started)
	})
	out := ListResult{Sessions: make([]SessionInfo, 0, len(items))}
	for _, item := range items {
		out.Sessions = append(out.Sessions, item.info)
	}
	return out, nil
}
func (m *Manager) pruneHistoryLocked() {
	type item struct {
		id    string
		ended time.Time
	}
	var finished []item
	for id, r := range m.history {
		_, _, ended := r.info()
		if ended.IsZero() {
			continue
		}
		if time.Since(ended) > 30*time.Minute {
			delete(m.history, id)
			continue
		}
		finished = append(finished, item{id, ended})
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].ended.Before(finished[j].ended) })
	for i := 0; i < len(finished)-10; i++ {
		delete(m.history, finished[i].id)
	}
}
func (m *Manager) Control(ctx context.Context, a ControlRequest) (ControlResult, error) {
	if err := ctx.Err(); err != nil {
		return ControlResult{}, err
	}
	if err := a.Validate(); err != nil {
		return ControlResult{}, err
	}
	r, p, err := m.lookup(a.SessionID)
	if err != nil {
		return ControlResult{}, err
	}
	if a.Action == "stop" {
		wait := 1000
		if a.WaitMS != nil {
			wait = *a.WaitMS
		}
		if p != nil {
			t := p.terminal
			t.mu.Lock()
			r.stopRequested()
			t.closed = true
			t.stopRetentionTimerLocked()
			t.mu.Unlock()
			p.stop()
		}
		return r.waitStopped(ctx, time.Duration(wait)*time.Millisecond)
	}
	if p == nil {
		return ControlResult{}, i18n.NewError("session.already.ended")
	}
	if a.Action != "input" && !slices.Contains(interactiveControls(), a.Action) {
		return ControlResult{}, i18n.NewError("session.control.unsupported")
	}
	if a.Action == "interrupt" {
		err = p.control(a.Action)
		return ControlResult{OK: err == nil}, err
	}
	if a.Action == "input" {
		if err = p.terminal.inputError(); err != nil {
			return ControlResult{}, err
		}
	}
	request := inputRequest{action: a.Action, done: make(chan error, 1)}
	if a.Input != nil {
		request.input = *a.Input
	}
	select {
	case p.inputQueue <- request:
	default:
		return ControlResult{}, i18n.NewError("session.input.queue.full")
	}
	if a.Action == "input" {
		return ControlResult{OK: true}, nil
	}
	select {
	case err := <-request.done:
		return ControlResult{OK: err == nil}, err
	case <-ctx.Done():
		return ControlResult{}, ctx.Err()
	case <-p.done:
		return ControlResult{}, i18n.NewError("session.already.ended")
	}
}

type inputRequest struct {
	action, input string
	done          chan error
}

func (m *Manager) inputWorker(ctx context.Context, p *process) {
	defer m.workers.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-p.inputQueue:
			if err := p.terminal.waitSubmitted(ctx); err != nil {
				request.done <- err
				return
			}
			var err error
			if request.action == "eof" {
				err = p.control("eof")
			} else {
				err = p.write(request.input)
			}
			request.done <- err
		}
	}
}
func (m *Manager) commandStarted(id, command string) CommandEvent {
	e := CommandEvent{ID: id, Command: command, Interactive: true, StartedAt: time.Now()}
	if m.callbacks.Command != nil {
		m.enqueueEvent(func() { m.callbacks.Command(e) })
	} else {
		m.enqueueEvent(func() { fmt.Fprintf(m.log, i18n.Text(i18n.Terminal(), "agenterm.running.interactive.s"), command) })
	}
	return e
}
func (m *Manager) commandFinished(e CommandEvent, code int, detail string) {
	e.Done, e.Duration, e.ExitCode, e.Error = true, time.Since(e.StartedAt), code, detail
	if m.callbacks.Command != nil {
		m.enqueueEvent(func() { m.callbacks.Command(e) })
	} else {
		m.enqueueEvent(func() { fmt.Fprintf(m.log, i18n.Text(i18n.Terminal(), "agenterm.finished.exit.code.d"), code) })
	}
}
func (m *Manager) enqueueEvent(event func()) {
	m.eventsMu.Lock()
	if !m.eventsClosed {
		m.events = append(m.events, event)
	}
	m.eventsMu.Unlock()
	select {
	case m.eventsWake <- struct{}{}:
	default:
	}
}
func (m *Manager) deliverEvents() {
	defer close(m.eventsDone)
	for {
		<-m.eventsWake
		for {
			m.eventsMu.Lock()
			events := m.events
			m.events = nil
			m.eventsBytes = 0
			close(m.eventsChanged)
			m.eventsChanged = make(chan struct{})
			closed := m.eventsClosed
			m.eventsMu.Unlock()
			for _, event := range events {
				event()
			}
			if closed {
				return
			}
			if len(events) == 0 {
				break
			}
		}
	}
}

// Observer output has a separate bounded queue. Slow UI/log consumers apply
// backpressure; manager cancellation releases producers before Close waits.
const maxObserverOutput = 1 << 20

func (m *Manager) forwardOutput(id string, data []byte) error {
	for len(data) > 0 {
		n := min(len(data), 64<<10)
		m.eventsMu.Lock()
		for !m.eventsClosed && m.eventsBytes+n > maxObserverOutput {
			changed := m.eventsChanged
			m.eventsMu.Unlock()
			select {
			case <-changed:
			case <-m.ctx.Done():
				return nil
			}
			m.eventsMu.Lock()
		}
		if m.eventsClosed || m.ctx.Err() != nil {
			m.eventsMu.Unlock()
			return nil
		}
		copied := append([]byte(nil), data[:n]...)
		m.eventsBytes += n
		m.events = append(m.events, func() {
			if m.callbacks.Output != nil {
				if id != "" {
					m.callbacks.Output(id, copied)
				}
			} else {
				_, _ = m.log.Write(copied)
			}
		})
		m.eventsMu.Unlock()
		select {
		case m.eventsWake <- struct{}{}:
		default:
		}
		data = data[n:]
	}
	return nil
}
func processResult(cmd *exec.Cmd, err error) (code int, detail string) {
	code = -1
	if cmd != nil && cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		detail = err.Error()
	}
	return
}
