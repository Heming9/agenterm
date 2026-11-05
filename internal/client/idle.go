package client

import (
	"context"
	"sync"
	"time"
)

// idleTracker excludes heartbeats and read-only polling and pauses expiry while
// a remote command is running. The deadline also survives transport retries.
type idleTracker struct {
	mu      sync.Mutex
	timeout time.Duration
	last    time.Time
	running map[string]bool
	wake    chan struct{}
}

func newIdleTracker(ctx context.Context, timeout time.Duration, expired func()) *idleTracker {
	t := &idleTracker{timeout: timeout, running: make(map[string]bool), wake: make(chan struct{}, 1)}
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.wake:
			case <-timer.C:
			}
			t.mu.Lock()
			remaining := timeout
			if !t.last.IsZero() && len(t.running) == 0 {
				remaining = time.Until(t.last.Add(timeout))
			}
			if remaining <= 0 {
				t.mu.Unlock()
				expired()
				return
			}
			t.mu.Unlock()
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(remaining)
		}
	}()
	return t
}
func (t *idleTracker) signal() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}
func (t *idleTracker) start() {
	t.mu.Lock()
	if t.last.IsZero() {
		t.last = time.Now()
	}
	t.mu.Unlock()
	t.signal()
}
func (t *idleTracker) touch() { t.mu.Lock(); t.last = time.Now(); t.mu.Unlock(); t.signal() }
func (t *idleTracker) command(e CommandEvent) {
	t.mu.Lock()
	if e.Done {
		delete(t.running, e.ID)
	} else {
		t.running[e.ID] = true
	}
	t.last = time.Now()
	t.mu.Unlock()
	t.signal()
}
