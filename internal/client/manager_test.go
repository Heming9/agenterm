package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(io.Discard, TerminalCallbacks{})
	t.Cleanup(func() { m.Close(); m.WaitCallbacks() })
	return m
}
func runAPI(t *testing.T, m *Manager, a ExecuteRequest) Output {
	t.Helper()
	out, err := m.Execute(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestAnonymousSessionIDsAndClosedOutput(t *testing.T) {
	m := testManager(t)
	for _, command := range []string{"echo complete", "exit 7"} {
		out := runAPI(t, m, ExecuteRequest{Command: command})
		if out.SessionID != "" || out.Status != "closed" || out.ExitCode == nil {
			t.Fatalf("%+v", out)
		}
	}
	// A closed shell's output stays available when the response must paginate.
	command := `awk 'BEGIN {for(i=0;i<20000;i++)printf "x"}'`
	if runtime.GOOS == "windows" {
		command = `[Console]::Write(('x' * 20000))`
	}
	out := runAPI(t, m, ExecuteRequest{Command: command})
	if out.SessionID == "" || !out.HasMore || out.Status != "closed" || out.Truncated || len(out.Output) != DefaultReadBytes {
		t.Fatalf("paged: %+v", out)
	}
	rest, err := m.Read(context.Background(), ReadRequest{SessionID: out.SessionID, CommandID: out.CommandID, Cursor: out.NextCursor})
	if err != nil || rest.HasMore || len(out.Output)+len(rest.Output) != 20000 {
		t.Fatalf("rest=%+v err=%v", rest, err)
	}
}
func TestClientOwnedStateAndProcessBudget(t *testing.T) {
	first, second := testManager(t), testManager(t)
	runAPI(t, first, ExecuteRequest{Setup: &Setup{SessionID: "same"}, Command: "echo first"})
	runAPI(t, second, ExecuteRequest{Setup: &Setup{SessionID: "same"}, Command: "echo second"})
	for i := 2; i < MaxTerminals; i++ {
		runAPI(t, first, ExecuteRequest{Setup: &Setup{SessionID: fmt.Sprint(i)}, Command: "echo ready"})
	}
	if _, err := second.Execute(context.Background(), ExecuteRequest{Command: "echo forbidden"}); err == nil {
		t.Fatal("process quota exceeded")
	}
	if _, err := first.Execute(context.Background(), ExecuteRequest{Setup: &Setup{SessionID: "same"}, Command: "echo forbidden"}); err == nil {
		t.Fatal("duplicate name accepted")
	}
	before, err := first.Read(context.Background(), ReadRequest{SessionID: "same"})
	if err != nil || before.ExitCode == nil || *before.ExitCode != 0 {
		t.Fatal(before, err)
	}
	stopped, err := first.Control(context.Background(), ControlRequest{SessionID: "same", Action: "stop", WaitMS: new(5000)})
	if err != nil || stopped.Status != "closed" {
		t.Fatal(stopped, err)
	}
	after, err := first.Read(context.Background(), ReadRequest{SessionID: "same"})
	if err != nil || after.ExitCode == nil || *after.ExitCode != 0 || after.Output != before.Output {
		t.Fatal(after, err)
	}
	runAPI(t, second, ExecuteRequest{Command: "echo replacement"})
}
func TestCanceledWaitPreservesExecution(t *testing.T) {
	m := testManager(t)
	command := "sleep 0.2; echo survived"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Milliseconds 200; Write-Output survived"
	}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() {
		_, err := m.Execute(ctx, ExecuteRequest{Setup: &Setup{SessionID: "wait"}, Command: command})
		ended <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		list, err := m.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Sessions) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not admitted")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-ended; err != context.Canceled {
		t.Fatal(err)
	}
	var out ReadOutput
	for {
		var err error
		out, err = m.Read(context.Background(), ReadRequest{SessionID: "wait", WaitMS: 1000})
		if err != nil {
			t.Fatal(err)
		}
		if out.Status == "idle" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(out)
		}
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(out.Output, "survived") || out.ExitCode == nil || *out.ExitCode != 0 {
		t.Fatal(out)
	}
}
func TestConcurrentAdmissionIsAtomic(t *testing.T) {
	m := testManager(t)
	var workers sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for i := 0; i < 20; i++ {
		workers.Go(func() {
			_, err := m.Execute(context.Background(), ExecuteRequest{Setup: &Setup{SessionID: "unique"}, Command: "echo once", WaitMS: new(0)})
			if err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		})
	}
	workers.Wait()
	if accepted != 1 {
		t.Fatal(accepted)
	}
}
func TestRecordPagingRetentionAndConfirmedExit(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprint(persistent), func(t *testing.T) {
			r := newRecord("output")
			r.begin("command", "large", persistent)
			data := strings.Repeat("界", (17<<20)/3)
			r.append([]byte(data))
			r.complete("command", 7, "")
			r.close(-1, "shell stopped")
			var all strings.Builder
			cursor := int64(0)
			for {
				out, err := r.read(context.Background(), ReadRequest{SessionID: "output", CommandID: "command", Cursor: cursor})
				if err != nil || !utf8.ValidString(out.Output) || out.ExitCode == nil || *out.ExitCode != 7 || out.Error != "" {
					t.Fatal(out, err)
				}
				if persistent && cursor == 0 && !out.Truncated {
					t.Fatal("missing truncation")
				}
				all.WriteString(out.Output)
				cursor = out.NextCursor
				if !out.HasMore {
					break
				}
			}
			if !persistent && all.String() != data {
				t.Fatal("anonymous output lost")
			}
			if persistent && all.Len() > maxSessionOutput {
				t.Fatal("history exceeded budget")
			}
			if _, err := r.read(context.Background(), ReadRequest{Cursor: cursor + 1}); err == nil {
				t.Fatal("future cursor accepted")
			}
			if _, err := r.read(context.Background(), ReadRequest{CommandID: "old"}); err == nil {
				t.Fatal("stale command accepted")
			}
		})
	}
	r := newRecord("boundary")
	r.begin("c", "unicode", true)
	r.append([]byte("界"))
	if _, err := r.read(context.Background(), ReadRequest{Cursor: 1}); err == nil {
		t.Fatal("partial rune cursor accepted")
	}
}
func TestHistoryRetentionOwnedByManager(t *testing.T) {
	m := testManager(t)
	m.mu.Lock()
	for i := 0; i < 15; i++ {
		r := newRecord(fmt.Sprint(i))
		r.begin("c", "done", false)
		r.close(0, "")
		r.ended = time.Now().Add(time.Duration(i-15) * time.Second)
		m.history[r.id] = r
	}
	old := newRecord("expired")
	old.close(0, "")
	old.ended = time.Now().Add(-time.Hour)
	m.history[old.id] = old
	m.mu.Unlock()
	list, err := m.List(context.Background())
	if err != nil || len(list.Sessions) != 10 {
		t.Fatal(list, err)
	}
	for _, s := range list.Sessions {
		if s.SessionID == "expired" {
			t.Fatal(s)
		}
	}
}
func TestRuntimeCommandCallbacksAndLog(t *testing.T) {
	var events []CommandEvent
	var mu sync.Mutex
	m := NewManager(io.Discard, TerminalCallbacks{Command: func(e CommandEvent) { mu.Lock(); events = append(events, e); mu.Unlock() }})
	out := runAPI(t, m, ExecuteRequest{Command: "echo lifecycle"})
	m.Close()
	m.WaitCallbacks()
	if len(events) != 2 || events[0].Done || !events[1].Done || events[0].ID != events[1].ID || events[0].StartedAt != events[1].StartedAt || events[1].Duration <= 0 || out.ExitCode == nil || events[1].ExitCode != *out.ExitCode {
		t.Fatal(events, out)
	}
	var log bytes.Buffer
	m = NewManager(&log, TerminalCallbacks{})
	runAPI(t, m, ExecuteRequest{Command: "echo marker"})
	m.Close()
	m.WaitCallbacks()
	if !strings.Contains(log.String(), "[Agenterm]") || !strings.Contains(log.String(), "marker") {
		t.Fatal(log.String())
	}
}

func TestEOFWaitEndsWithProcess(t *testing.T) {
	if !slices.Contains(Controls(), "eof") {
		t.Skip("EOF unavailable")
	}
	m := testManager(t)
	processCtx, cancel := context.WithCancel(context.Background())
	p := &process{cancel: cancel, done: processCtx.Done(), ready: make(chan struct{}), inputQueue: make(chan inputRequest, 1)}
	r := newRecord("queued")
	r.begin("c", "waiting", true)
	p.terminal = &persistentTerminal{c: m, p: p, id: "queued", history: r}
	m.mu.Lock()
	m.running[r.id] = p
	m.history[r.id] = r
	m.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := m.Control(context.Background(), ControlRequest{SessionID: r.id, Action: "eof"})
		done <- err
	}()
	select {
	case <-p.inputQueue:
	case <-time.After(time.Second):
		t.Fatal("EOF not queued")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unprocessed EOF succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("EOF waiter leaked")
	}
}
func TestCommandCallbackCanReadAndExecute(t *testing.T) {
	var m *Manager
	done := make(chan error, 1)
	m = NewManager(io.Discard, TerminalCallbacks{Command: func(e CommandEvent) {
		if !e.Done || e.Command != "echo first" {
			return
		}
		if _, err := m.List(context.Background()); err != nil {
			done <- err
			return
		}
		out, err := m.Execute(context.Background(), ExecuteRequest{SessionID: "chain", Command: "echo second"})
		if err == nil && !strings.Contains(out.Output, "second") {
			err = fmt.Errorf("missing chained output: %+v", out)
		}
		done <- err
	}})
	t.Cleanup(func() { m.Close(); m.WaitCallbacks() })
	runAPI(t, m, ExecuteRequest{Setup: &Setup{SessionID: "chain"}, Command: "echo first"})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("callback reentry blocked runtime")
	}
}

func TestCallbackMayCloseManager(t *testing.T) {
	var m *Manager
	closed := make(chan struct{})
	m = NewManager(io.Discard, TerminalCallbacks{Command: func(e CommandEvent) {
		if e.Done {
			m.Close()
			close(closed)
		}
	}})
	runAPI(t, m, ExecuteRequest{Command: "echo close"})
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited on its own callback")
	}
	m.WaitCallbacks()
}

func TestObserverOutputBackpressureAndClose(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m := NewManager(io.Discard, TerminalCallbacks{Output: func(string, []byte) { once.Do(func() { close(entered) }); <-release }})
	// Stall the consumer, fill its queue, and prove cancellation releases a producer.
	if err := m.forwardOutput("c", []byte("start")); err != nil {
		t.Fatal(err)
	}
	<-entered
	for range maxObserverOutput / (64 << 10) {
		_ = m.forwardOutput("c", make([]byte, 64<<10))
	}
	blocked := make(chan struct{})
	go func() { _ = m.forwardOutput("c", []byte("blocked")); close(blocked) }()
	select {
	case <-blocked:
		t.Error("output budget did not apply backpressure")
	case <-time.After(20 * time.Millisecond):
	}
	m.Close()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Error("shutdown stranded output producer")
	}
	close(release)
	m.WaitCallbacks()
}
