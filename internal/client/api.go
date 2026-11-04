// Package client owns local terminal execution and connection adapters.
package client

import "context"

const APIVersion = "1"

const (
	DefaultWaitMS    = 10000
	MaxWaitMS        = 20000
	MinReadBytes     = 4
	DefaultReadBytes = 16 << 10
	MaxReadBytes     = 64 << 10
)

// API defines terminal operations independently of their transport.
type API interface {
	Execute(context.Context, ExecuteRequest) (Output, error)
	Read(context.Context, ReadRequest) (ReadOutput, error)
	List(context.Context) (ListResult, error)
	Control(context.Context, ControlRequest) (ControlResult, error)
}

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
type ListResult struct {
	Sessions []SessionInfo `json:"sessions"`
}
type ControlResult struct {
	OK       bool   `json:"ok,omitempty"`
	Status   string `json:"status,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}
