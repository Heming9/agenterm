package client

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
	"context"
	"sort"
	"time"
)

// History belongs to this connection, just like its shells. Reconnection starts
// with neither old shells nor old results.
func (c *connection) pruneHistoryLocked() {
	type finished struct {
		id    string
		ended time.Time
	}
	var closed []finished
	for id, record := range c.history {
		ended := record.Ended()
		if ended.IsZero() {
			continue
		}
		if time.Since(ended) > 30*time.Minute {
			delete(c.history, id)
			continue
		}
		closed = append(closed, finished{id, ended})
	}
	sort.Slice(closed, func(i, j int) bool { return closed[i].ended.Before(closed[j].ended) })
	for i := 0; i < len(closed)-10; i++ {
		delete(c.history, closed[i].id)
	}
}

func (c *connection) startHistoryRead(m protocol.Message) {
	c.mu.Lock()
	c.pruneHistoryLocked()
	record := c.history[m.SessionID]
	ctx, cancel := context.WithCancel(context.Background())
	if c.readers == nil {
		c.readers = make(map[string]context.CancelFunc)
	}
	c.readers[m.ID] = cancel
	c.mu.Unlock()
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		defer cancel()
		defer func() { c.mu.Lock(); delete(c.readers, m.ID); c.mu.Unlock() }()
		reply := protocol.Message{Type: "session_result", ID: m.ID}
		var err error
		if record == nil {
			err = i18n.NewError("unknown.session")
		} else if m.Read == nil {
			err = i18n.NewError("session.read.invalid.arguments")
		} else if m.Type == "session_wait" {
			reply.Result, err = record.Wait(ctx, *m.Read)
		} else {
			reply.Result, err = record.Read(ctx, *m.Read)
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			reply.Error = err.Error()
		}
		_ = c.send(reply)
	}()
}
