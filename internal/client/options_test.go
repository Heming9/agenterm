package client

import (
	"errors"
	"flag"
	"io"
	"testing"
)

func TestParseOptions(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		for _, args := range [][]string{
			{"-token", " token "}, {"-token= token "},
			{"-invite", " invitation "}, {"-invite= invitation "},
		} {
			flags := flag.NewFlagSet("client", flag.ContinueOnError)
			flags.SetOutput(io.Discard)
			options, err := ParseOptions(flags, args, terminal, func(s string) string { return s })
			if err != nil {
				t.Fatal(err)
			}
			if options.Token != "token" && options.Invitation != "invitation" {
				t.Fatalf("args %v: unexpected options %+v", args, options)
			}
		}
	}
}

func TestConnectionMode(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		mode    string
		wantErr bool
	}{
		{nil, ConnectionModeTemporary, false},
		{[]string{"-token=example"}, ConnectionModeTemporary, false},
		{[]string{"-connection-mode=temporary"}, ConnectionModeTemporary, false},
		{[]string{"-connection-mode", "persistent"}, ConnectionModePersistent, false},
		{[]string{"-connection-mode=invalid"}, "", true},
		{[]string{"-connection-mode="}, "", true},
		{[]string{"-temporary"}, "", true},
	} {
		flags := flag.NewFlagSet("client", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		options, err := ParseOptions(flags, tc.args, true, func(s string) string { return s })
		if (err != nil) != tc.wantErr || (!tc.wantErr && options.ConnectionMode != tc.mode) {
			t.Fatalf("args %v: options %+v, error %v", tc.args, options, err)
		}
	}
}

func TestParseOptionsValidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
		args     []string
		wantErr  bool
	}{
		{"interactive GUI", false, nil, false},
		{"interactive CLI", true, nil, false},
		{"installer", true, []string{"-invite=invitation", "-consent-confirmed", "-plain"}, false},
		{"plain CLI", true, []string{"-plain"}, false},
		{"plain GUI", false, []string{"-plain"}, true},
		{"GUI consent bypass", false, []string{"-invite=invitation", "-consent-confirmed"}, true},
		{"missing invitation", true, []string{"-consent-confirmed"}, true},
		{"conflicting credentials", true, []string{"-token=secret", "-invite=other"}, true},
		{"GUI conflicting credentials", false, []string{"-token=secret", "-invite=other"}, true},
		{"unknown", false, []string{"-unknown"}, true},
		{"missing value", false, []string{"-invite"}, true},
		{"positional", true, []string{"secret"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := flag.NewFlagSet("client", flag.ContinueOnError)
			flags.SetOutput(io.Discard)
			options, err := ParseOptions(flags, tc.args, tc.terminal, func(s string) string { return s })
			if (err != nil) != tc.wantErr {
				t.Fatalf("options %+v, error %v", options, err)
			}
			if tc.name == "installer" && (!options.Plain || !options.ConsentConfirmed) {
				t.Fatalf("installer options lost: %+v", options)
			}
		})
	}
}

func TestParseOptionsHelp(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		flags := flag.NewFlagSet("client", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		_, err := ParseOptions(flags, []string{"-help"}, terminal, func(s string) string { return s })
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("help error: %v", err)
		}
	}
}
