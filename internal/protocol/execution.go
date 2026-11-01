package protocol

import (
	"agenterm/internal/i18n"
	"strings"
)

const DefaultCommandTimeoutMS = 60_000
const MaxCommandTimeoutMS = 24 * 60 * 60 * 1000

// ExecOptions apply to one command only. Env overrides the inherited environment.
// A zero timeout leaves the command running until it exits or is canceled.
type ExecOptions struct {
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TimeoutMS int64             `json:"timeout_ms,omitempty"`
}

func (o ExecOptions) Validate() error {
	if o.TimeoutMS < 0 || o.TimeoutMS > MaxCommandTimeoutMS {
		return i18n.NewError("execution.timeout.invalid", MaxCommandTimeoutMS)
	}
	if strings.ContainsRune(o.Cwd, 0) {
		return i18n.NewError("execution.cwd.invalid")
	}
	for key, value := range o.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return i18n.NewError("execution.env.invalid")
		}
	}
	return nil
}

// CommandResult is returned as MCP structuredContent and as JSON text.
type CommandResult struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	TimedOut  bool   `json:"timed_out"`
	Canceled  bool   `json:"canceled"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"`
}

func (r CommandResult) Failed() bool {
	return r.ExitCode != 0 || r.TimedOut || r.Canceled || r.Error != ""
}
