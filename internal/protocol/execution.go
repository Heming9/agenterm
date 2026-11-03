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

// ValidateTerminalName is shared by admission and the public API. Names are
// scoped to a client connection; IDs always identify one immutable shell.
func ValidateTerminalName(name string) error {
	if name == "" {
		return nil
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 64 || strings.TrimSpace(name) == "" {
		return fmt.Errorf("invalid terminal_name: use 1–64 printable characters")
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("invalid terminal_name: use 1–64 printable characters")
		}
	}
	return nil
}
