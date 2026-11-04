package client

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestControlValidationBelongsToAPI(t *testing.T) {
	for _, a := range []ControlRequest{
		{}, {SessionID: "s", Action: "input"}, {SessionID: "s", Action: "input", Input: new(""), WaitMS: new(0)},
		{SessionID: "s", Action: "eof", Input: new("")}, {SessionID: "s", Action: "interrupt", WaitMS: new(0)},
		{SessionID: "s", Action: "stop", WaitMS: new(20001)}, {SessionID: "s", Action: "unsupported"},
	} {
		if err := a.Validate(); err == nil {
			t.Fatal(a)
		}
	}
	for _, a := range []ControlRequest{{SessionID: "s", Action: "input", Input: new("")}, {SessionID: "s", Action: "interrupt"}, {SessionID: "s", Action: "eof"}, {SessionID: "s", Action: "stop"}, {SessionID: "s", Action: "stop", WaitMS: new(0)}} {
		if err := a.Validate(); err != nil {
			t.Fatal(a, err)
		}
	}
}
func TestSetupValidation(t *testing.T) {
	for _, setup := range []Setup{{SessionID: "bad\nname"}, {SessionID: strings.Repeat("x", 65)}, {ExecOptions: ExecOptions{Cwd: "bad\x00"}}, {ExecOptions: ExecOptions{Env: map[string]string{"A=B": "bad"}}}} {
		if err := ValidateSessionRequest("", &setup); err == nil {
			t.Fatal(setup)
		}
	}
	if err := ValidateSessionRequest("reuse", &Setup{}); err == nil {
		t.Fatal("reuse and creation accepted together")
	}
	if err := ValidateSessionRequest("", &Setup{SessionID: "中文 空格"}); err != nil {
		t.Fatal(err)
	}
}
func TestDispatchRejectsUnknownEnvelopeData(t *testing.T) {
	// A nil implementation must never be called for malformed requests.
	for _, data := range []string{
		`{"method":"retired","params":{}}`,
		`{"method":"execute","params":{"legacy":true}}`,
		`{"method":"list","params":{} {}}`,
		`{"method":"list","params":{},"legacy":true}`,
		`{"method":"list","params":{}} {}`,
	} {
		if _, err := DispatchAPI(context.Background(), nil, json.RawMessage(data)); err == nil {
			t.Fatal(string(data))
		}
	}
}
