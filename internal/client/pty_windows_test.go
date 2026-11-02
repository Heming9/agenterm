package client

import (
	"context"
	"io"
	"os"
	"testing"
)

func TestWindowsEOFClosesOnlyInputAndRejectsInterrupt(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &process{cancel: cancel, ready: make(chan struct{})}
	p.setIO(writer, reader)
	if err := p.control("interrupt"); err == nil {
		t.Fatal("pipe session accepted terminal interrupt")
	}
	if err := p.control("eof"); err != nil {
		t.Fatal(err)
	}
	if err := p.control("eof"); err != nil {
		t.Fatal("repeated stdin close failed", err)
	}
	if _, err := reader.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("stdin was not closed: %v", err)
	}
	if err := p.write("late"); err != io.ErrClosedPipe {
		t.Fatalf("write after EOF: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("EOF canceled the process context")
	}
}
