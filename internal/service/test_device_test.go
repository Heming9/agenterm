package service

import (
	"agenterm/internal/protocol"
	terminal "agenterm/internal/terminalapi"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
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
	if r.Action == "input" && r.Input != nil {
		out.Output += a.label + ":" + *r.Input
		a.sessions[r.SessionID] = out
	}
	return terminal.ControlResult{OK: true}, nil
}
func serveAPI(ws *testDeviceConn, api deviceAPI) {
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
				data, err := dispatchFixtureAPI(callCtx, api, m.Request)
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
	deviceAPI
	execute func(context.Context, terminal.ExecuteRequest) (terminal.Output, error)
	read    func(context.Context, terminal.ReadRequest) (terminal.ReadOutput, error)
	list    func(context.Context) (terminal.ListResult, error)
	control func(context.Context, terminal.ControlRequest) (terminal.ControlResult, error)
}

func (a testAPI) Execute(c context.Context, r terminal.ExecuteRequest) (terminal.Output, error) {
	if a.execute != nil {
		return a.execute(c, r)
	}
	return a.deviceAPI.Execute(c, r)
}
func (a testAPI) Read(c context.Context, r terminal.ReadRequest) (terminal.ReadOutput, error) {
	if a.read != nil {
		return a.read(c, r)
	}
	return a.deviceAPI.Read(c, r)
}
func (a testAPI) List(c context.Context) (terminal.ListResult, error) {
	if a.list != nil {
		return a.list(c)
	}
	return a.deviceAPI.List(c)
}
func (a testAPI) Control(c context.Context, r terminal.ControlRequest) (terminal.ControlResult, error) {
	if a.control != nil {
		return a.control(c, r)
	}
	return a.deviceAPI.Control(c, r)
}

type deviceAPI interface {
	Execute(context.Context, terminal.ExecuteRequest) (terminal.Output, error)
	Read(context.Context, terminal.ReadRequest) (terminal.ReadOutput, error)
	List(context.Context) (terminal.ListResult, error)
	Control(context.Context, terminal.ControlRequest) (terminal.ControlResult, error)
}

// dispatchFixtureAPI routes wire requests to the in-memory test device.
func dispatchFixtureAPI(ctx context.Context, api deviceAPI, data json.RawMessage) (json.RawMessage, error) {
	var request terminal.APIRequest
	if err := decodeFixtureRequest(data, &request); err != nil {
		return nil, err
	}
	var result any
	var err error
	switch request.Method {
	case "execute":
		var a terminal.ExecuteRequest
		if err = decodeFixtureRequest(request.Params, &a); err == nil {
			result, err = api.Execute(ctx, a)
		}
	case "read":
		var a terminal.ReadRequest
		if err = decodeFixtureRequest(request.Params, &a); err == nil {
			result, err = api.Read(ctx, a)
		}
	case "list":
		var a struct{}
		if err = decodeFixtureRequest(request.Params, &a); err == nil {
			result, err = api.List(ctx)
		}
	case "control":
		var a terminal.ControlRequest
		if err = decodeFixtureRequest(request.Params, &a); err == nil {
			result, err = api.Control(ctx, a)
		}
	default:
		err = fmt.Errorf("unknown client API method %q", request.Method)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}
func decodeFixtureRequest(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("invalid trailing request data")
	}
	return nil
}
