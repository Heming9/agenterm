package service

import (
	"agenterm/internal/protocol"
	"agenterm/internal/terminalhistory"
	"context"
	"sync"

	"github.com/gorilla/websocket"
)

// Simulated devices own their result history, like the real client. Output is
// deliberately not sent unsolicited to the server.
type testDeviceConn struct {
	*websocket.Conn
	mu      sync.Mutex
	history map[string]*terminalhistory.Record
}

func wrapTestDevice(ws *websocket.Conn) *testDeviceConn {
	return &testDeviceConn{Conn: ws, history: make(map[string]*terminalhistory.Record)}
}
func (w *testDeviceConn) WriteJSON(value any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	m, ok := value.(protocol.Message)
	if !ok {
		return w.Conn.WriteJSON(value)
	}
	if m.Type == "command_accepted" {
		record := w.history[m.SessionID]
		if record == nil || m.SessionCreate {
			record = terminalhistory.New(m.SessionID)
			w.history[m.SessionID] = record
		}
		m.Cursor = record.Begin(m.RequestID, m.Command, m.SessionPersistent)
	} else if record := w.history[m.ID]; record != nil {
		record.Accept(m)
		if m.Type == "output" {
			return nil
		}
	}
	return w.Conn.WriteJSON(m)
}
func (w *testDeviceConn) ReadJSON(value any) error {
	for {
		var m protocol.Message
		if err := w.Conn.ReadJSON(&m); err != nil {
			return err
		}
		if m.Type == "session_read_cancel" {
			continue
		}
		if m.Type != "session_read" && m.Type != "session_wait" {
			*(value.(*protocol.Message)) = m
			return nil
		}
		w.mu.Lock()
		record := w.history[m.SessionID]
		w.mu.Unlock()
		go func(m protocol.Message) {
			reply := protocol.Message{Type: "session_result", ID: m.ID}
			var err error
			if record == nil {
				reply.Error = "unknown session"
			} else if m.Type == "session_wait" {
				reply.Result, err = record.Wait(context.Background(), *m.Read)
			} else {
				reply.Result, err = record.Read(context.Background(), *m.Read)
			}
			if err != nil {
				reply.Error = err.Error()
			}
			_ = w.WriteJSON(reply)
		}(m)
	}
}
