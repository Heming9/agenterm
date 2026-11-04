package client

import (
	"agenterm/internal/i18n"
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"agenterm/internal/terminalhistory"
	"agenterm/internal/version"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// CommandEvent describes one command's lifecycle. Done distinguishes a completion
// from a start; Duration includes process startup and is only set on completion.
type CommandEvent struct {
	ID          string
	Command     string
	Interactive bool
	StartedAt   time.Time
	Done        bool
	Duration    time.Duration
	ExitCode    int
	Error       string
}

// Callbacks may be invoked concurrently by running commands. When Command is
// supplied, it replaces the plain-text command headers and footers in logWriter;
// Output, when supplied, receives process output with its command ID; otherwise
// process output is written to logWriter as it arrives.
type Callbacks struct {
	// Redeemed receives the reusable token after an invitation is exchanged.
	Redeemed     func(token string)
	Connecting   func()
	Connected    func()
	Reconnecting func(ReconnectEvent)
	Latency      func(time.Duration)
	Command      func(CommandEvent)
	Output       func(id string, data []byte)
}

type connection struct {
	ws    *websocket.Conn
	write sync.Mutex
	mu    sync.Mutex

	history  map[string]*terminalhistory.Record
	readers  map[string]context.CancelFunc
	running  map[string]*process
	log      io.Writer
	command  func(CommandEvent)
	output   func(string, []byte)
	workers  sync.WaitGroup
	outputMu sync.Mutex
}

func (c *connection) commandStarted(m protocol.Message) CommandEvent {
	e := CommandEvent{ID: m.ID, Command: m.Command, Interactive: m.Interactive, StartedAt: time.Now()}
	if c.command != nil {
		c.command(e)
	} else if m.Interactive {
		fmt.Fprintf(c.log, i18n.Text(i18n.Terminal(), "agenterm.running.interactive.s"), m.Command)
	} else {
		fmt.Fprintf(c.log, i18n.Text(i18n.Terminal(), "agenterm.running.s"), m.Command)
	}
	return e
}

func (c *connection) commandFinished(e CommandEvent, code int, detail string) {
	e.Done, e.Duration, e.ExitCode, e.Error = true, time.Since(e.StartedAt), code, detail
	if c.command != nil {
		c.command(e)
	} else {
		fmt.Fprintf(c.log, i18n.Text(i18n.Terminal(), "agenterm.finished.exit.code.d"), code)
	}
}

func (c *connection) send(m protocol.Message) error {
	c.write.Lock()
	defer c.write.Unlock()
	if err := c.ws.SetWriteDeadline(time.Now().Add(protocol.WriteTimeout)); err != nil {
		return err
	}
	return c.ws.WriteJSON(m)
}

type process struct {
	ownsTerminalSlot bool
	terminalSlotOnce sync.Once
	terminal         *persistentTerminal
	cancel           context.CancelFunc
	mu               sync.Mutex
	inputMu          sync.Mutex
	ready            chan struct{}
	readyOnce        sync.Once
	input            io.WriteCloser
	output           io.ReadCloser
	stopped          bool
	inputEOF         bool
	inputQueue       chan protocol.Message
}

func (p *process) stop() {
	p.cancel()
	p.mu.Lock()
	p.stopped = true
	input, output := p.input, p.output
	p.mu.Unlock()
	p.readyOnce.Do(func() { close(p.ready) })
	if input != nil {
		_ = input.Close()
	}
	if output != nil {
		_ = output.Close()
	}
}

func (p *process) setIO(input io.WriteCloser, output io.ReadCloser) {
	p.mu.Lock()
	p.input, p.output = input, output
	stopped := p.stopped
	p.mu.Unlock()
	p.readyOnce.Do(func() { close(p.ready) })
	if stopped {
		_ = input.Close()
		_ = output.Close()
	}
}

func (p *process) write(input string) error {
	// Session input may arrive before the shell has finished starting.
	<-p.ready
	p.inputMu.Lock()
	defer p.inputMu.Unlock()
	p.mu.Lock()
	writer := p.input
	closed := p.stopped || p.inputEOF
	p.mu.Unlock()
	if writer == nil || closed {
		return io.ErrClosedPipe
	}
	_, err := io.WriteString(writer, input)
	return err
}

func (p *process) control(action string) error {
	<-p.ready
	// Interrupt must not wait for a blocked stdin write; it is an OS signal.
	if action == "eof" {
		p.inputMu.Lock()
		defer p.inputMu.Unlock()
	}
	p.mu.Lock()
	input, stopped, eof := p.input, p.stopped, p.inputEOF
	p.mu.Unlock()
	if input == nil || stopped {
		return i18n.NewError("session.already.ended")
	}
	if action == "eof" && eof {
		return nil // Closing a pipe's stdin a second time is harmless.
	}
	closed, err := controlInteractive(input, action)
	if closed && err == nil {
		p.mu.Lock()
		p.inputEOF = true
		p.mu.Unlock()
	}
	return err
}

func (c *connection) replyControl(p *process, m protocol.Message) {
	err := error(i18n.NewError("session.already.ended"))
	if p != nil {
		err = p.control(m.Type)
	}
	reply := protocol.Message{Type: "control_done", ID: m.RequestID}
	if err != nil {
		reply.Error = err.Error()
	}
	_ = c.send(reply)
}

// Serialize input and EOF per process without blocking the WebSocket reader.
// Interrupt/cancel must remain usable while a program is not consuming stdin.
func (c *connection) inputWorker(ctx context.Context, p *process) {
	defer c.workers.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-p.inputQueue:
			if p.terminal != nil {
				if p.terminal.waitSubmitted(ctx) != nil {
					return
				}
			}
			if m.Type == "eof" {
				c.replyControl(p, m)
			} else if err := p.write(m.Input); err != nil {
				_ = c.send(protocol.Message{Type: "output", ID: m.ID, Stream: "stderr", Output: fmt.Sprintf(i18n.Text(i18n.Terminal(), "client.input.error"), err)})
			}
		}
	}
}

type outputWriter struct {
	c      *connection
	id     string
	stream string
}

func (o outputWriter) Write(p []byte) (int, error) {
	if err := o.c.forwardOutput(protocol.Message{Type: "output", ID: o.id, Stream: o.stream}, o.id, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// An empty command ID keeps idle terminal output out of command callbacks.
func (c *connection) forwardOutput(m protocol.Message, commandID string, data []byte) error {
	c.outputMu.Lock()
	defer c.outputMu.Unlock()
	if c.output != nil {
		if commandID != "" {
			c.output(commandID, data)
		}
	} else {
		_, _ = c.log.Write(data)
	}
	if m.Stream == "terminal" {
		return nil
	}
	m.Output = string(data)
	return c.send(m)
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

func (c *connection) run(m protocol.Message) {
	// Legacy execution paths share the same resource budget. P2P bootstraps
	// are transport helpers rather than managed terminals.
	if m.P2P == nil && !acquireTerminalSlot() {
		_ = c.send(protocol.Message{Type: "done", ID: m.ID, ExitCode: -1, Error: i18n.Text(i18n.Terminal(), "session.limit.reached.try.again.later")})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	if m.P2P != nil {
		original := cancel
		var once sync.Once
		cancel = func() { once.Do(func() { stopP2P(m.P2P.StopScript); original() }) }
		m.Command = m.P2P.Script
		m.Env = map[string]string{"AGENTERM_P2P_CONFIG_B64": m.P2P.ConfigBase64}
	}
	p := &process{cancel: cancel, ready: make(chan struct{}), inputQueue: make(chan protocol.Message, 32), ownsTerminalSlot: m.P2P == nil}
	c.mu.Lock()
	c.running[m.ID] = p
	c.mu.Unlock()
	c.workers.Add(1)
	if m.Interactive {
		c.workers.Add(1)
		go c.inputWorker(ctx, p)
	}
	go func() {
		defer c.workers.Done()
		defer p.releaseTerminalSlot()
		defer func() { p.stop(); c.mu.Lock(); delete(c.running, m.ID); c.mu.Unlock() }()
		stopIO := context.AfterFunc(ctx, p.stop)
		defer stopIO()
		display := m
		if m.P2P != nil {
			display.Command = "p2p-cli start"
		}
		event := c.commandStarted(display)
		err := m.ExecOptions.Validate()
		var cmd *exec.Cmd
		if err == nil {
			cmd = shellCommand(ctx, m.Command)
			if m.P2P != nil {
				cmd = p2pCommand(ctx, m.Command)
			}
			cmd.Dir = m.Cwd
			cmd.Env = cmd.Environ()
			for key, value := range m.Env {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			configureCommand(cmd, m.Interactive)
			if m.Interactive {
				var input io.WriteCloser
				var output io.ReadCloser
				input, output, err = startInteractive(cmd)
				if err == nil {
					p.setIO(input, output)
					writer := newOutputWriter(outputWriter{c: c, id: m.ID, stream: "terminal"})
					_, _ = io.Copy(writer, output)
					_ = writer.Close()
					err = cmd.Wait()
				}
			} else {
				stdout := newOutputWriter(outputWriter{c: c, id: m.ID, stream: "stdout"})
				stderr := newOutputWriter(outputWriter{c: c, id: m.ID, stream: "stderr"})
				cmd.Stdout, cmd.Stderr = stdout, stderr
				err = cmd.Run()
				_ = stdout.Close()
				_ = stderr.Close()
			}
		}
		code, detail := processResult(cmd, err)
		canceled := errors.Is(ctx.Err(), context.Canceled)
		if canceled {
			code, detail = -1, i18n.Text(i18n.Terminal(), "execution.canceled")
		} else if detail != "" {
			code = -1
		}
		c.commandFinished(event, code, detail)
		p.releaseTerminalSlot()
		_ = c.send(protocol.Message{Type: "done", ID: m.ID, ExitCode: code, Error: detail, Canceled: canceled})
	}()
}

// RunWithCallbacks runs a connection with structured lifecycle notifications for
// consumers such as the desktop UI, retaining the local lock across retries.
func RunWithCallbacks(ctx context.Context, token string, logWriter io.Writer, callbacks Callbacks) error {
	return runWithCallbacks(ctx, token, false, logWriter, callbacks)
}

// RunInvitationWithCallbacks reserves this user's connection to the server
// before redeeming the invitation, holding the reservation until disconnection.
// The caller must obtain user consent before calling this function.
func RunInvitationWithCallbacks(ctx context.Context, invitation string, logWriter io.Writer, callbacks Callbacks) error {
	return runWithCallbacks(ctx, invitation, true, logWriter, callbacks)
}

func runWithCallbacks(ctx context.Context, token string, invitation bool, logWriter io.Writer, callbacks Callbacks) error {
	t, err := protocol.Decode(token)
	if err != nil {
		return err
	}
	lock, err := acquireConnectionLock(t.URL)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if invitation {
		token, err = invite.Redeem(ctx, token)
		if err != nil {
			return err
		}
		if callbacks.Redeemed != nil {
			callbacks.Redeemed(token)
		}
		t, err = protocol.Decode(token)
		if err != nil {
			return err
		}
	}
	return reconnect(ctx, t, logWriter, callbacks)
}

func connectOnce(ctx context.Context, t protocol.Token, logWriter io.Writer, callbacks Callbacks) (result attemptResult) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+t.Secret)
	headers.Set(version.Header, version.Current)
	hostname, _ := os.Hostname()
	headers.Set(protocol.HostnameHeader, base64.RawURLEncoding.EncodeToString([]byte(hostname)))
	headers.Set(protocol.PlatformHeader, runtime.GOOS)
	headers.Set(protocol.ArchHeader, runtime.GOARCH)
	headers.Set(protocol.SessionControlsHeader, strings.Join(interactiveControls(), ","))
	headers.Set(protocol.P2PCapabilityHeader, "1")
	headers.Set(protocol.PersistentSessionsHeader, protocol.PersistentSessionsVersion)
	headers.Set(protocol.ShellHeader, base64.RawURLEncoding.EncodeToString([]byte(Shell())))
	cwd, _ := os.Getwd()
	headers.Set(protocol.DefaultCwdHeader, base64.RawURLEncoding.EncodeToString([]byte(cwd)))
	ws, resp, err := websocket.DefaultDialer.DialContext(ctx, t.ConnectionURL(), headers)
	if err != nil {
		if resp != nil {
			if resp.Body != nil {
				resp.Body.Close()
			}
			if resp.StatusCode == http.StatusUpgradeRequired {
				if versionErr := version.Check(version.Current, resp.Header.Get(version.Header)); versionErr != nil {
					return attemptResult{err: versionErr, permanent: true}
				}
			}
			return attemptResult{err: i18n.NewError("connect.w.http.d", err, resp.StatusCode), permanent: !retryHTTPStatus(resp.StatusCode), retryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now())}
		}
		return attemptResult{err: err}
	}
	defer ws.Close()
	if err := version.Check(version.Current, resp.Header.Get(version.Header)); err != nil {
		return attemptResult{err: err, permanent: true}
	}
	c := &connection{ws: ws, running: make(map[string]*process), log: logWriter, command: callbacks.Command, output: callbacks.Output}
	defer func() {
		// Close I/O before waiting, so old command callbacks finish before the
		// next connection can start. Commands are never replayed on reconnect.
		ws.Close()
		c.mu.Lock()
		for _, cancel := range c.readers {
			cancel()
		}
		for _, p := range c.running {
			p.stop()
		}
		c.mu.Unlock()
		c.workers.Wait()
	}()
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-closed:
				return
			case <-ticker.C:
				c.mu.Lock()
				c.pruneHistoryLocked()
				c.mu.Unlock()
			}
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "client disconnected"), time.Now().Add(time.Second))
			ws.Close()
		case <-closed:
		}
	}()
	if callbacks.Connected != nil {
		callbacks.Connected()
	}
	started := time.Now()
	defer func() { result.online = time.Since(started) }()
	// Older servers do not advertise heartbeats. Keep connecting to them
	// without imposing a read timeout they cannot satisfy.
	heartbeat := resp.Header.Get(protocol.HeartbeatHeader) == "1"
	refreshDeadline := func() error {
		if heartbeat {
			return ws.SetReadDeadline(time.Now().Add(protocol.HeartbeatTimeout))
		}
		return nil
	}
	defaultPing := ws.PingHandler()
	ws.SetPingHandler(func(payload string) error {
		if err := refreshDeadline(); err != nil {
			return err
		}
		return defaultPing(payload)
	})
	_ = refreshDeadline()
	ws.SetReadLimit(2 << 20)
	for {
		var m protocol.Message
		if err := ws.ReadJSON(&m); err != nil {
			if ctx.Err() != nil {
				return attemptResult{}
			}
			return attemptResult{err: i18n.NewError("disconnected.w", err), permanent: websocket.IsCloseError(err, websocket.ClosePolicyViolation, websocket.CloseProtocolError, websocket.CloseUnsupportedData)}
		}
		_ = refreshDeadline()
		switch m.Type {
		case "latency":
			if callbacks.Latency != nil && m.LatencyMS >= 0 && m.LatencyMS <= protocol.HeartbeatTimeout.Milliseconds() {
				callbacks.Latency(time.Duration(m.LatencyMS) * time.Millisecond)
			}
		case "session_wait", "session_read":
			c.startHistoryRead(m)
		case "session_read_cancel":
			c.mu.Lock()
			if cancel := c.readers[m.ID]; cancel != nil {
				cancel()
			}
			c.mu.Unlock()
		case "session_exec":
			c.runPersistent(m)
		case "exec":
			c.run(m)
		case "p2p_start":
			if m.P2P != nil && len(m.P2P.Script) <= 64<<10 && len(m.P2P.StopScript) <= 64<<10 && len(m.P2P.ConfigBase64) <= 32<<10 {
				c.run(m)
			}
		case "cancel":
			c.mu.Lock()
			p := c.running[m.ID]
			c.mu.Unlock()
			if p != nil {
				if p.terminal != nil {
					p.terminal.mu.Lock()
					p.terminal.history.StopRequested()
					p.terminal.closed = true
					p.terminal.stopRetentionTimerLocked()
					p.terminal.mu.Unlock()
				}
				p.stop()
			} else {
				_ = c.send(protocol.Message{Type: "done", ID: m.ID, ExitCode: -1, Error: i18n.Text(i18n.Terminal(), "session.already.ended")})
			}
		case "interrupt":
			c.mu.Lock()
			p := c.running[m.ID]
			c.mu.Unlock()
			c.workers.Add(1)
			go func() { defer c.workers.Done(); c.replyControl(p, m) }()
		case "input", "eof":
			c.mu.Lock()
			p := c.running[m.ID]
			c.mu.Unlock()
			detail := i18n.Text(i18n.Terminal(), "session.already.ended")
			allowed := p != nil
			if allowed && m.Type == "input" && p.terminal != nil {
				if err := p.terminal.inputError(); err != nil {
					detail = err.Error()
					allowed = false
				}
			}
			if allowed {
				select {
				case p.inputQueue <- m:
					// Input acceptance confirms validation and queueing, not a
					// potentially blocking stdin write or program consumption.
					if m.Type == "input" && m.RequestID != "" {
						_ = c.send(protocol.Message{Type: "control_done", ID: m.RequestID})
					}
					continue
				default:
					detail = i18n.Text(i18n.Terminal(), "session.input.queue.full")
				}
			}
			if m.RequestID != "" {
				_ = c.send(protocol.Message{Type: "control_done", ID: m.RequestID, Error: detail})
			} else {
				_ = c.send(protocol.Message{Type: "output", ID: m.ID, Stream: "stderr", Output: detail})
			}

		}
	}
}
