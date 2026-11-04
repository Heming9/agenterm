package client

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
	"context"
	"fmt"
	"time"
)

// This adapter owns only request cancellation and serialization. Canceling a
// caller's wait never closes a terminal or changes a confirmed command result.
func (c *connection) startAPICall(m protocol.Message) {
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.readers[m.ID] = cancel
	c.mu.Unlock()
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		defer cancel()
		defer func() { c.mu.Lock(); delete(c.readers, m.ID); c.mu.Unlock() }()
		reply := protocol.Message{Type: "api_result", ID: m.ID}
		if m.Request == nil {
			reply.Error = "missing client API request"
		} else {
			data, err := DispatchAPI(ctx, c.api, m.Request)
			if err != nil {
				reply.Error = err.Error()
			} else {
				reply.Data = data
			}
		}
		if ctx.Err() == nil {
			_ = c.send(reply)
		}
	}()
}

type bootstrapOutput struct {
	c  *connection
	id string
}

func (o bootstrapOutput) Write(data []byte) (int, error) {
	o.c.outputMu.Lock()
	defer o.c.outputMu.Unlock()
	if o.c.output != nil {
		o.c.output(o.id, data)
	} else {
		_, _ = o.c.log.Write(data)
	}
	return len(data), nil
}
func (c *connection) startBootstrap(m protocol.Message) {
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	if c.bootstraps[m.ID] != nil {
		c.mu.Unlock()
		cancel()
		return
	}
	c.bootstraps[m.ID] = cancel
	c.mu.Unlock()
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		defer cancel()
		defer func() { c.mu.Lock(); delete(c.bootstraps, m.ID); c.mu.Unlock() }()
		event := CommandEvent{ID: m.ID, Command: "p2p-cli start", StartedAt: time.Now()}
		if c.command != nil {
			c.command(event)
		} else {
			fmt.Fprintf(c.log, i18n.Text(i18n.Terminal(), "agenterm.running.s"), event.Command)
		}
		code, err := Bootstrap(ctx, BootstrapOptions{Script: m.P2P.Script, StopScript: m.P2P.StopScript, ConfigBase64: m.P2P.ConfigBase64}, bootstrapOutput{c, m.ID})
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		event.Done, event.Duration, event.ExitCode, event.Error = true, time.Since(event.StartedAt), code, detail
		if c.command != nil {
			c.command(event)
		} else {
			fmt.Fprintf(c.log, i18n.Text(i18n.Terminal(), "agenterm.finished.exit.code.d"), code)
		}
		_ = c.send(protocol.Message{Type: "p2p_done", ID: m.ID})
	}()
}
