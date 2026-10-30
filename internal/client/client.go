package client

import (
	"agenterm/internal/protocol"
	"context"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"sync"
)

type connection struct {
	ws      *websocket.Conn
	write   sync.Mutex
	mu      sync.Mutex
	running map[string]*process
	log     io.Writer
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
	_, _ = o.c.log.Write(p)
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
		fmt.Fprintf(c.log, "\n[Agenterm] Running: %s\n", m.Command)
		shell, args := "/bin/sh", []string{"-c", m.Command}
		if runtime.GOOS == "windows" {
			shell, args = "cmd.exe", []string{"/C", m.Command}
		}
		cmd := exec.CommandContext(ctx, shell, args...)
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
		fmt.Fprintf(c.log, "\n[Agenterm] Finished (exit code: %d)\n", code)
		_ = c.send(protocol.Message{Type: "done", ID: m.ID, ExitCode: code, Error: detail})
	}()
}
func (c *connection) runInteractive(m protocol.Message) {
	ctx, cancel := context.WithCancel(context.Background())
	fmt.Fprintf(c.log, "\n[Agenterm] Running (interactive): %s\n", m.Command)
	shell, args := "/bin/sh", []string{"-c", m.Command}
	if runtime.GOOS == "windows" {
		shell, args = "cmd.exe", []string{"/C", m.Command}
	}
	cmd := exec.CommandContext(ctx, shell, args...)
	input, output, err := startInteractive(cmd)
	if err != nil {
		cancel()
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
		fmt.Fprintf(c.log, "\n[Agenterm] Finished (exit code: %d)\n", code)
		_ = c.send(protocol.Message{Type: "done", ID: m.ID, ExitCode: code, Error: detail})
	}()
}

// Run connects until the context is canceled or the connection is lost.
// onConnected is called after the WebSocket handshake. The caller owns logWriter.
func Run(ctx context.Context, token string, logWriter io.Writer, onConnected func()) error {
	t, err := protocol.Decode(token)
	if err != nil {
		return err
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+t.Secret)
	ws, resp, err := websocket.DefaultDialer.DialContext(ctx, t.URL, headers)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("connect: %w (HTTP %d)", err, resp.StatusCode)
		}
		return err
	}
	defer ws.Close()
	c := &connection{ws: ws, running: make(map[string]*process), log: logWriter}
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
	if onConnected != nil {
		onConnected()
	}
	ws.SetReadLimit(2 << 20)
	for {
		var m protocol.Message
		if err := ws.ReadJSON(&m); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("disconnected: %w", err)
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
					_ = c.send(protocol.Message{Type: "output", ID: m.ID, Output: "\n[Agenterm] input error: " + err.Error() + "\n"})
				}
			}
		}
	}
}
