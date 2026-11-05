package client

import (
	"flag"
	"fmt"
	"strings"
)

const (
	ConnectionModeTemporary  = "temporary"
	ConnectionModePersistent = "persistent"
)

// Options contains startup settings shared by the client entry points.
type Options struct {
	Token            string
	Invitation       string
	Plain            bool
	ConsentConfirmed bool
	ConnectionMode   string
}

// ParseOptions registers and parses client flags. Terminal-only flags are
// registered only by the CLI, so the GUI rejects them rather than ignoring them.
// The caller owns help/error presentation and the authorization UI.
func ParseOptions(flags *flag.FlagSet, args []string, terminal bool, text func(string) string) (Options, error) {
	var options Options
	flags.StringVar(&options.Token, "token", "", text("connection.token.omit.to.enter.interactively"))
	flags.StringVar(&options.Invitation, "invite", "", text("one.time.invitation"))
	if terminal {
		flags.StringVar(&options.ConnectionMode, "connection-mode", ConnectionModeTemporary, text("connection.mode.flag"))
		flags.BoolVar(&options.Plain, "plain", false, text("use.plain.text.output"))
		flags.BoolVar(&options.ConsentConfirmed, "consent-confirmed", false, text("connection.consent.already.obtained.by.the.installer"))
	}
	if err := flags.Parse(args); err != nil {
		return Options{}, err
	}
	if flags.NArg() != 0 {
		return Options{}, fmt.Errorf("unexpected positional arguments; use -token or -invite")
	}
	if terminal && options.ConnectionMode != ConnectionModeTemporary && options.ConnectionMode != ConnectionModePersistent {
		return Options{}, fmt.Errorf("-connection-mode must be temporary or persistent")
	}
	options.Token = strings.TrimSpace(options.Token)
	options.Invitation = strings.TrimSpace(options.Invitation)
	if options.Token != "" && options.Invitation != "" {
		return Options{}, fmt.Errorf("-token and -invite cannot be used together")
	}
	if options.ConsentConfirmed && options.Invitation == "" {
		return Options{}, fmt.Errorf("-consent-confirmed requires -invite")
	}
	return options, nil
}
