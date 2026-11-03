package protocol

import (
	"agenterm/internal/i18n"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxTerminalsPerClient bounds live terminals in a client process. Finished
// session records and P2P bootstrap processes do not consume terminal slots.
const MaxTerminalsPerClient = 10

// ExecOptions initialize a new terminal. Commands run until they exit or the
// session is explicitly stopped; a tool's wait_ms never limits execution.
type ExecOptions struct {
	Cwd string            `json:"cwd,omitempty"`
	Env map[string]string `json:"env,omitempty"`
}

func (o ExecOptions) Validate() error {
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

// ValidateSessionID accepts both caller-supplied and generated session IDs.
func ValidateSessionID(name string) error {
	if name == "" {
		return nil
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 64 || strings.TrimSpace(name) == "" {
		return fmt.Errorf("invalid session_id: use 1–64 printable characters")
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("invalid session_id: use 1–64 printable characters")
		}
	}
	return nil
}

// SessionSetup always requests creation. An omitted ID follows the anonymous
// terminal lifetime; an explicit ID creates a persistent terminal.
type SessionSetup struct {
	SessionID string `json:"session_id,omitempty"`
	ExecOptions
}

func ValidateSessionRequest(id string, setup *SessionSetup) error {
	if err := ValidateSessionID(id); err != nil {
		return err
	}
	if setup == nil {
		return nil
	}
	if id != "" {
		return i18n.NewError("session.setup.conflict")
	}
	if err := ValidateSessionID(setup.SessionID); err != nil {
		return err
	}
	return setup.ExecOptions.Validate()
}
