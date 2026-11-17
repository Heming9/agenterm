// Package terminalapi defines the wire contract for terminal requests and responses.
package terminalapi

import "encoding/json"

const APIVersion = "1"

const (
	DefaultWaitMS              = 10000
	MaxWaitMS                  = 20000
	MinReadBytes               = 4
	DefaultReadBytes           = 16 << 10
	MaxReadBytes               = 64 << 10
	SessionCommandPreviewChars = 160
)

type ExecuteRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Command   string `json:"command"`
	Setup     *Setup `json:"setup,omitempty"`
	WaitMS    *int   `json:"wait_ms,omitempty"`
}
type ReadRequest struct {
	SessionID string `json:"session_id"`
	CommandID string `json:"command_id,omitempty"`
	Cursor    int64  `json:"cursor,omitempty"`
	WaitMS    int    `json:"wait_ms,omitempty"`
	MaxBytes  *int   `json:"max_bytes,omitempty"`
}
type ControlRequest struct {
	SessionID string  `json:"session_id"`
	Action    string  `json:"action"`
	Input     *string `json:"input,omitempty"`
	WaitMS    *int    `json:"wait_ms,omitempty"`
}
type Output struct {
	SessionID  string `json:"session_id,omitempty"`
	CommandID  string `json:"command_id"`
	Status     string `json:"status"`
	Output     string `json:"output"`
	NextCursor int64  `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	Error      string `json:"error,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// ReadOutput always identifies the session, even when its shell has exited.
type ReadOutput struct {
	SessionID  string `json:"session_id"`
	CommandID  string `json:"command_id"`
	Status     string `json:"status"`
	Output     string `json:"output"`
	NextCursor int64  `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	Error      string `json:"error,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}
type SessionInfo struct {
	SessionID string `json:"session_id"`
	Command   string `json:"command"`
	CommandID string `json:"command_id"`
	Status    string `json:"status"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Summarize bounds display metadata without changing the submitted command.
// The relay also applies this to responses from older clients.
func (s *SessionInfo) Summarize() {
	const suffix = "... (truncated)"
	n, cut := 0, 0
	for offset := range s.Command {
		if n == SessionCommandPreviewChars-len(suffix) {
			cut = offset
		}
		if n == SessionCommandPreviewChars {
			s.Command = s.Command[:cut] + suffix
			return
		}
		n++
	}
}

type ListResult struct {
	Sessions []SessionInfo `json:"sessions"`
}
type ControlResult struct {
	OK       bool   `json:"ok,omitempty"`
	Status   string `json:"status,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ExecOptions initialize a new terminal. Commands run until they exit or the
// session is explicitly stopped; a tool's wait_ms never limits execution.
type ExecOptions struct {
	Cwd string            `json:"cwd,omitempty"`
	Env map[string]string `json:"env,omitempty"`
}

// Setup always requests creation. An omitted ID follows the anonymous
// terminal lifetime; an explicit ID creates a persistent terminal.
type Setup struct {
	SessionID string `json:"session_id,omitempty"`
	ExecOptions
}

// APIRequest can be carried unchanged over a relay or a direct connection.
type APIRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
