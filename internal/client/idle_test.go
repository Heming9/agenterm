package client

import (
	"context"
	"testing"
	"time"
)

func TestTemporaryIdleLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	expired := make(chan struct{})
	tracker := newIdleTracker(ctx, 150*time.Millisecond, func() { close(expired) })
	tracker.start()
	tracker.command(CommandEvent{ID: "command"})
	select {
	case <-expired:
		t.Fatal("expired while command was running")
	case <-time.After(220 * time.Millisecond):
	}
	tracker.command(CommandEvent{ID: "command", Done: true})
	time.Sleep(90 * time.Millisecond)
	tracker.touch()
	select {
	case <-expired:
		t.Fatal("activity did not reset timeout")
	case <-time.After(90 * time.Millisecond):
	}
	select {
	case <-expired:
	case <-time.After(time.Second):
		t.Fatal("idle connection did not expire")
	}
}
