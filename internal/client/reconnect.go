package client

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type ReconnectEvent struct {
	Attempt int
	Delay   time.Duration
	Online  time.Duration
	Err     error
}

func (e ReconnectEvent) Text(lang string) string {
	return fmt.Sprintf(i18n.Text(lang, "connection.retry"), e.Online.Round(time.Second), i18n.Localize(e.Err, lang), e.Attempt, e.Delay.Round(time.Second))
}

type attemptResult struct {
	err        error
	permanent  bool
	retryAfter time.Duration
	online     time.Duration
}

func reconnect(ctx context.Context, token protocol.Token, log io.Writer, callbacks Callbacks) error {
	if log == nil {
		log = io.Discard
	}
	if callbacks.IdleTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		tracker := newIdleTracker(ctx, callbacks.IdleTimeout, func() {
			if callbacks.IdleDisconnected != nil {
				callbacks.IdleDisconnected()
			}
			cancel()
		})
		callbacks.idle = tracker
		originalCommand := callbacks.Command
		callbacks.Command = func(e CommandEvent) {
			tracker.command(e)
			if originalCommand != nil {
				originalCommand(e)
			} else if e.Done {
				fmt.Fprintf(log, i18n.Text(i18n.Terminal(), "agenterm.finished.exit.code.d"), e.ExitCode)
			} else if e.Interactive {
				fmt.Fprintf(log, i18n.Text(i18n.Terminal(), "agenterm.running.interactive.s"), e.Command)
			} else {
				fmt.Fprintf(log, i18n.Text(i18n.Terminal(), "agenterm.running.s"), e.Command)
			}
		}
	}
	api := NewManager(log, TerminalCallbacks{Command: callbacks.Command, Output: callbacks.Output})
	defer func() { api.Close(); api.WaitCallbacks() }()
	resumeCredential := ""
	delay, attempt := time.Second, 0
	for ctx.Err() == nil {
		if callbacks.Connecting != nil {
			callbacks.Connecting()
		}
		result := connectOnce(ctx, token, log, callbacks, api, &resumeCredential)
		if ctx.Err() != nil {
			return nil
		}
		if result.err == nil || result.permanent {
			return result.err
		}
		if result.online >= protocol.HeartbeatTimeout {
			delay, attempt = time.Second, 0
		}
		attempt++
		wait := max(delay, result.retryAfter)
		event := ReconnectEvent{Attempt: attempt, Delay: wait, Online: result.online, Err: result.err}
		if callbacks.Reconnecting != nil {
			callbacks.Reconnecting(event)
		} else {
			fmt.Fprintf(log, "\n%s %s\n", time.Now().Format(time.RFC3339), event.Text(i18n.Terminal()))
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		delay = nextRetryDelay(delay)
	}
	return nil
}

func nextRetryDelay(delay time.Duration) time.Duration { return min(30*time.Second, 2*delay) }

func retryHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if until, err := http.ParseTime(value); err == nil && until.After(now) {
		return until.Sub(now)
	}
	return 0
}
