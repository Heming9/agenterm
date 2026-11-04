package client

import (
	"agenterm/internal/i18n"
	"context"
	"io"
	"sync"
)

type process struct {
	terminalSlotOnce sync.Once
	terminal         *persistentTerminal
	cancel           context.CancelFunc
	done             <-chan struct{}
	mu               sync.Mutex
	inputMu          sync.Mutex
	ready            chan struct{}
	readyOnce        sync.Once
	input            io.WriteCloser
	output           io.ReadCloser
	stopped          bool
	inputEOF         bool
	inputQueue       chan inputRequest
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
