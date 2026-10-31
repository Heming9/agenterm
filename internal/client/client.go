package client

import (
	"agenterm/internal/i18n"
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
	"net/http"
	"os/exec"
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
	Redeemed  func(token string)
	Connected func()
	Command   func(CommandEvent)
	Output    func(id string, data []byte)
}

type connection struct {
	ws      *websocket.Conn
	write   sync.Mutex
	mu      sync.Mutex
	running map[string]*process
	log     io.Writer
	command func(CommandEvent)
	output  func(string, []byte)
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
	return c.ws.WriteJSON(m)
}

type process struct {
	cancel context.CancelFunc
	input  io.WriteCloser
}

type outputWriter struct {
	c  *connection
	id string
}

func (o outputWriter) Write(p []byte) (int, error) {
	if o.c.output != nil {
		o.c.output(o.id, p)
	} else {
		_, _ = o.c.log.Write(p)
	}
	if err := o.c.send(protocol.Message{Type: "output", ID: o.id, Output: string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (c *connection) run(m protocol.Message) {
	if m.Interactive {
		c.runInteractive(m)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.running[m.ID] = &process{cancel: cancel}
	c.mu.Unlock()
	go func() {
		defer func() { cancel(); c.mu.Lock(); delete(c.running, m.ID); c.mu.Unlock() }()
		event := c.commandStarted(m)
		cmd := shellCommand(ctx, m.Command)
		writer := newOutputWriter(outputWriter{c: c, id: m.ID})
		cmd.Stdout = writer
		cmd.Stderr = writer
		cmd.Stdin = nil
		err := cmd.Run()
		_ = writer.Close()
		code := 0
		detail := ""
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				code = -1
				detail = err.Error()
			}
		}
		c.commandFinished(event, code, detail)
		_ = c.send(protocol.Message{Type: "done", ID: m.ID, ExitCode: code, Error: detail})
	}()
}
func (c *connection) runInteractive(m protocol.Message) {
	ctx, cancel := context.WithCancel(context.Background())
	event := c.commandStarted(m)
	cmd := shellCommand(ctx, m.Command)
	input, output, err := startInteractive(cmd)
	if err != nil {
		cancel()
		c.commandFinished(event, -1, err.Error())
		_ = c.send(protocol.Message{Type: "done", ID: m.ID, ExitCode: -1, Error: err.Error()})
		return
	}
	c.mu.Lock()
	c.running[m.ID] = &process{cancel: cancel, input: input}
	c.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			input.Close()
			output.Close()
			c.mu.Lock()
			delete(c.running, m.ID)
			c.mu.Unlock()
		}()
		writer := newOutputWriter(outputWriter{c: c, id: m.ID})
		_, _ = io.Copy(writer, output)
		_ = writer.Close()
		err := cmd.Wait()
		code := 0
		detail := ""
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				code = -1
				detail = err.Error()
			}
		}
		c.commandFinished(event, code, detail)
		_ = c.send(protocol.Message{Type: "done", ID: m.ID, ExitCode: code, Error: detail})
	}()
}

// Run connects until the context is canceled or the connection is lost.
// onConnected is called after the WebSocket handshake. The caller owns logWriter.
func Run(ctx context.Context, token string, logWriter io.Writer, onConnected func()) error {
	return RunWithCallbacks(ctx, token, logWriter, Callbacks{Connected: onConnected})
}

// RunWithCallbacks runs a connection with structured lifecycle notifications for
// consumers such as the desktop UI. A lost connection still requires reconnecting.
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
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+t.Secret)
	headers.Set(version.Header, version.Current)
	ws, resp, err := websocket.DefaultDialer.DialContext(ctx, t.URL, headers)
	if err != nil {
		if resp != nil {
			if resp.StatusCode == http.StatusUpgradeRequired {
				if versionErr := version.Check(version.Current, resp.Header.Get(version.Header)); versionErr != nil {
					return versionErr
				}
			}
			return i18n.NewError("connect.w.http.d", err, resp.StatusCode)
		}
		return err
	}
	defer ws.Close()
	if err := version.Check(version.Current, resp.Header.Get(version.Header)); err != nil {
		return err
	}
	c := &connection{ws: ws, running: make(map[string]*process), log: logWriter, command: callbacks.Command, output: callbacks.Output}
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, p := range c.running {
			p.cancel()
		}
	}()
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-ctx.Done():
			ws.Close()
		case <-closed:
		}
	}()
	if callbacks.Connected != nil {
		callbacks.Connected()
	}
	ws.SetReadLimit(2 << 20)
	for {
		var m protocol.Message
		if err := ws.ReadJSON(&m); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return i18n.NewError("disconnected.w", err)
		}
		switch m.Type {
		case "exec":
			c.run(m)
		case "cancel":
			c.mu.Lock()
			p := c.running[m.ID]
			c.mu.Unlock()
			if p != nil {
				p.cancel()
			}
		case "input":
			c.mu.Lock()
			p := c.running[m.ID]
			c.mu.Unlock()
			if p != nil && p.input != nil {
				if _, err := io.WriteString(p.input, m.Input); err != nil && !errors.Is(err, io.ErrClosedPipe) {
					_ = c.send(protocol.Message{Type: "output", ID: m.ID, Output: fmt.Sprintf(i18n.Text(i18n.Terminal(), "client.input.error"), err)})
				}
			}
		}
	}
}
