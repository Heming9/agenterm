package main

import (
	"agenterm/internal/protocol"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// startHeartbeat is called before the read loop. Only one Ping is outstanding;
// its monotonic send time measures RTT without synchronizing the two clocks.
func (c *client) startHeartbeat(interval, timeout time.Duration) func() {
	var mu sync.Mutex
	var pending string
	var sent time.Time
	stop, done := make(chan struct{}), make(chan struct{})
	_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
	c.conn.SetPongHandler(func(payload string) error {
		mu.Lock()
		if pending == "" || payload != pending {
			mu.Unlock()
			return nil
		}
		rtt := time.Since(sent)
		pending = ""
		mu.Unlock()
		if err := c.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
		latencyMS := max(1, rtt.Milliseconds())
		c.mu.Lock()
		c.latencyMS = latencyMS
		c.mu.Unlock()
		return c.send(protocol.Message{Type: "latency", LatencyMS: latencyMS})
	})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			mu.Lock()
			payload := ""
			if pending == "" {
				sent = time.Now()
				pending = strconv.FormatInt(sent.UnixNano(), 10)
				payload = pending
			}
			mu.Unlock()
			if payload != "" {
				if err := c.conn.WriteControl(websocket.PingMessage, []byte(payload), time.Now().Add(protocol.WriteTimeout)); err != nil {
					log.Printf("client heartbeat failed id=%s reason=%q", c.info.ID, err)
					c.conn.Close()
					return
				}
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { close(stop); <-done }
}
