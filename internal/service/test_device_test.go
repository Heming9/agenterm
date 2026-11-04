package service

import (
	terminal "agenterm/internal/client"
	"agenterm/internal/protocol"
	"context"

	"fmt"
	"github.com/gorilla/websocket"
	"sync"
	"unicode/utf8"
)

type testDeviceConn struct {
	*websocket.Conn
	write sync.Mutex
}

func wrapTestDevice(ws *websocket.Conn) *testDeviceConn { return &testDeviceConn{Conn: ws} }
func (w *testDeviceConn) WriteJSON(v any) error {
	w.write.Lock()
	defer w.write.Unlock()
	return w.Conn.WriteJSON(v)
}

type fixtureAPI struct {
	mu       sync.Mutex
	label    string
	sessions map[string]terminal.ReadOutput
	commands map[string]string
	arrived  chan<- string
	release  <-chan struct{}
}

func newFixtureAPI(label string) *fixtureAPI {
	return &fixtureAPI{label: label, sessions: map[string]terminal.ReadOutput{}, commands: map[string]string{}}
}
func (a *fixtureAPI) Execute(ctx context.Context, r terminal.ExecuteRequest) (terminal.Output, error) {
	if err := terminal.ValidateSessionRequest(r.SessionID, r.Setup); err != nil {
		return terminal.Output{}, err
	}
	if r.Command == "parallel" {
		a.arrived <- a.label
		select {
		case <-a.release:
		case <-ctx.Done():
			return terminal.Output{}, ctx.Err()
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	id := r.SessionID
	explicit := id != ""
	persistent := explicit || r.WaitMS != nil && *r.WaitMS == 0
	if r.Setup != nil {
		id = r.Setup.SessionID
		explicit = id != ""
		persistent = persistent || explicit
		if a.sessions[id].SessionID != "" {
			return terminal.Output{}, fmt.Errorf("session exists")
		}
	}
	if r.SessionID != "" && a.sessions[id].SessionID == "" {
		return terminal.Output{}, fmt.Errorf("unknown session")
	}
	if id == "" {
		id = random()
	}
	status := "closed"
	if persistent {
		status = "idle"
	}
	code := new(0)
	if r.Command == "interactive" {
		status = "running"
		code = nil
	}
	text := a.label + ":" + r.Command
	previous := a.sessions[id]
	out := terminal.ReadOutput{SessionID: id, CommandID: random(), Status: status, Output: previous.Output + text, NextCursor: int64(len(previous.Output + text)), ExitCode: code}
	a.sessions[id] = out
	a.commands[id] = r.Command
	result := terminal.Output(out)
	result.Output = text
	if !persistent {
		result.SessionID = ""
	}
	return result, nil
}
func (a *fixtureAPI) Read(ctx context.Context, r terminal.ReadRequest) (terminal.ReadOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out, ok := a.sessions[r.SessionID]
	if !ok {
		return out, fmt.Errorf("unknown session")
	}
	if r.CommandID != "" && r.CommandID != out.CommandID {
		return terminal.ReadOutput{}, fmt.Errorf("command changed")
	}
	limit := terminal.DefaultReadBytes
	if r.MaxBytes != nil {
		limit = *r.MaxBytes
	}
	if limit < 4 || limit > terminal.MaxReadBytes || r.Cursor < 0 || r.Cursor > int64(len(out.Output)) {
		return terminal.ReadOutput{}, fmt.Errorf("invalid read")
	}
	end := min(len(out.Output), int(r.Cursor)+limit)
	for end < len(out.Output) && !utf8.RuneStart(out.Output[end]) {
		end--
	}
	out.Output = out.Output[r.Cursor:end]
	out.NextCursor = int64(end)
	out.HasMore = end < len(a.sessions[r.SessionID].Output)
	return out, nil
}
func (a *fixtureAPI) List(context.Context) (terminal.ListResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := terminal.ListResult{Sessions: []terminal.SessionInfo{}}
	for id, r := range a.sessions {
		out.Sessions = append(out.Sessions, terminal.SessionInfo{SessionID: id, Command: a.commands[id], CommandID: r.CommandID, Status: r.Status, ExitCode: r.ExitCode})
	}
	return out, nil
}
func (a *fixtureAPI) Control(ctx context.Context, r terminal.ControlRequest) (terminal.ControlResult, error) {
	if err := r.Validate(); err != nil {
		return terminal.ControlResult{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out, ok := a.sessions[r.SessionID]
	if !ok {
		return terminal.ControlResult{}, fmt.Errorf("unknown session")
	}
	if r.Action == "stop" {
		out.Status = "closed"
		out.ExitCode = new(0)
		a.sessions[r.SessionID] = out
		return terminal.ControlResult{Status: "closed", ExitCode: new(0)}, nil
	}
	if out.Status == "closed" {
		return terminal.ControlResult{}, fmt.Errorf("closed")
	}
	if r.Action == "input" {
		out.Output += a.label + ":" + *r.Input
		a.sessions[r.SessionID] = out
	}
	return terminal.ControlResult{OK: true}, nil
}
func serveAPI(ws *testDeviceConn, api terminal.API) {
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	waiters := map[string]context.CancelFunc{}
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	for {
		var m protocol.Message
		if ws.ReadJSON(&m) != nil {
			return
		}
		switch m.Type {
		case "api_call":
			callCtx, stop := context.WithCancel(ctx)
			mu.Lock()
			waiters[m.ID] = stop
			mu.Unlock()
			workers.Go(func() {
				defer stop()
				data, err := terminal.DispatchAPI(callCtx, api, m.Request)
				reply := protocol.Message{Type: "api_result", ID: m.ID, Data: data}
				if err != nil {
					reply.Error = err.Error()
				}
				mu.Lock()
				delete(waiters, m.ID)
				mu.Unlock()
				_ = ws.WriteJSON(reply)
			})
		case "api_cancel":
			mu.Lock()
			if stop := waiters[m.ID]; stop != nil {
				stop()
			}
			mu.Unlock()
		case "p2p_start":
			if registerTestP2P(m.P2P.ConfigBase64) != nil {
				return
			}
		}
	}
}
func serveTestDevice(ws *testDeviceConn, label string, arrived chan<- string, release <-chan struct{}) {
	api := newFixtureAPI(label)
	api.arrived = arrived
	api.release = release
	serveAPI(ws, api)
}

// testAPI customizes individual client operations without adding relay state.
type testAPI struct {
	terminal.API
	execute func(context.Context, terminal.ExecuteRequest) (terminal.Output, error)
	read    func(context.Context, terminal.ReadRequest) (terminal.ReadOutput, error)
	list    func(context.Context) (terminal.ListResult, error)
	control func(context.Context, terminal.ControlRequest) (terminal.ControlResult, error)
}

func (a testAPI) Execute(c context.Context, r terminal.ExecuteRequest) (terminal.Output, error) {
	if a.execute != nil {
		return a.execute(c, r)
	}
	return a.API.Execute(c, r)
}
func (a testAPI) Read(c context.Context, r terminal.ReadRequest) (terminal.ReadOutput, error) {
	if a.read != nil {
		return a.read(c, r)
	}
	return a.API.Read(c, r)
}
func (a testAPI) List(c context.Context) (terminal.ListResult, error) {
	if a.list != nil {
		return a.list(c)
	}
	return a.API.List(c)
}
func (a testAPI) Control(c context.Context, r terminal.ControlRequest) (terminal.ControlResult, error) {
	if a.control != nil {
		return a.control(c, r)
	}
	return a.API.Control(c, r)
}
