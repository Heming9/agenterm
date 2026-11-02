package client

import (
	"agenterm/internal/i18n"
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
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
	ws       *websocket.Conn
	write    sync.Mutex
	mu       sync.Mutex
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
	cancel    context.CancelFunc
	mu        sync.Mutex
	ready     chan struct{}
	readyOnce sync.Once
	input     io.WriteCloser
	output    io.ReadCloser
	stopped   bool
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
	// session_write may arrive before the shell has finished starting.
	<-p.ready
	p.mu.Lock()
	writer := p.input
	p.mu.Unlock()
	if writer == nil {
		return nil
	}
	_, err := io.WriteString(writer, input)
	return err
}

type outputWriter struct {
	c      *connection
	id     string
	stream string
}

func (o outputWriter) Write(p []byte) (int, error) {
	o.c.outputMu.Lock()
	defer o.c.outputMu.Unlock()
	if o.c.output != nil {
		o.c.output(o.id, p)
	} else {
		_, _ = o.c.log.Write(p)
	}
	if err := o.c.send(protocol.Message{Type: "output", ID: o.id, Output: string(p), Stream: o.stream}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *connection) run(m protocol.Message) {
	ctx, cancel := context.WithCancel(context.Background())
	if m.TimeoutMS > 0 && m.TimeoutMS <= protocol.MaxCommandTimeoutMS {
		cancel()
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(m.TimeoutMS)*time.Millisecond)
	}
	p := &process{cancel: cancel, ready: make(chan struct{})}
	c.mu.Lock()
	c.running[m.ID] = p
	c.mu.Unlock()
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		defer func() { p.stop(); c.mu.Lock(); delete(c.running, m.ID); c.mu.Unlock() }()
		stopIO := context.AfterFunc(ctx, p.stop)
		defer stopIO()
		event := c.commandStarted(m)
		err := m.ExecOptions.Validate()
		code := -1
		if err == nil {
			cmd := shellCommand(ctx, m.Command)
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
			if cmd.ProcessState != nil {
				code = cmd.ProcessState.ExitCode()
			}
		}
		detail := ""
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
		canceled := errors.Is(ctx.Err(), context.Canceled)
		if timedOut || canceled {
			code = -1
		} else if err != nil {
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) {
				code, detail = -1, err.Error()
			}
		}
		if timedOut {
			detail = i18n.Text(i18n.Terminal(), "execution.timed.out")
		}
		if canceled {
			detail = i18n.Text(i18n.Terminal(), "execution.canceled")
		}
		c.commandFinished(event, code, detail)
		_ = c.send(protocol.Message{Type: "done", ID: m.ID, ExitCode: code, Error: detail, TimedOut: timedOut, Canceled: canceled})
	}()
}

// Run reconnects after transient failures until canceled or permanently rejected.
// onConnected is called after the WebSocket handshake. The caller owns logWriter.
func Run(ctx context.Context, token string, logWriter io.Writer, onConnected func()) error {
	return RunWithCallbacks(ctx, token, logWriter, Callbacks{Connected: onConnected})
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
	headers.Set(protocol.ShellHeader, base64.RawURLEncoding.EncodeToString([]byte(Shell())))
	cwd, _ := os.Getwd()
	headers.Set(protocol.DefaultCwdHeader, base64.RawURLEncoding.EncodeToString([]byte(cwd)))
	ws, resp, err := websocket.DefaultDialer.DialContext(ctx, t.URL, headers)
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
		for _, p := range c.running {
			p.stop()
		}
		c.mu.Unlock()
		c.workers.Wait()
	}()
	closed := make(chan struct{})
	defer close(closed)
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
		case "exec":
			c.run(m)
		case "cancel":
			c.mu.Lock()
			p := c.running[m.ID]
			c.mu.Unlock()
			if p != nil {
				p.stop()
			}
		case "input":
			c.mu.Lock()
			p := c.running[m.ID]
			c.mu.Unlock()
			if p != nil {
				if err := p.write(m.Input); err != nil && !errors.Is(err, io.ErrClosedPipe) {
					_ = c.send(protocol.Message{Type: "output", ID: m.ID, Stream: "stderr", Output: fmt.Sprintf(i18n.Text(i18n.Terminal(), "client.input.error"), err)})
				}
			}
		}
	}
}
