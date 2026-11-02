package main

import (
	"agenterm/internal/protocol"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// These types define the wire contracts, including optional terminal fields.
// Legacy map-based handlers keep their JSON shape and are checked against these
// explicit schemas by the MCP SDK.
type clientsResult struct {
	Clients    []clientInfo `json:"clients"`
	Status     string       `json:"status,omitempty"`
	NextAction string       `json:"next_action,omitempty"`
}

type sessionStartResult struct {
	SessionID string `json:"session_id"`
	Cursor    int64  `json:"cursor"`
}

type sessionReadResult struct {
	SessionID  string `json:"session_id"`
	Output     string `json:"output"`
	NextCursor int64  `json:"next_cursor"`
	Truncated  bool   `json:"truncated"`
	Done       bool   `json:"done"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	TimedOut   *bool  `json:"timed_out,omitempty"`
	Canceled   *bool  `json:"canceled,omitempty"`
	Error      string `json:"error,omitempty"`
}

type sessionWriteResult struct {
	Accepted bool `json:"accepted"`
}

type sessionStopResult struct {
	SessionID   string `json:"session_id"`
	Accepted    bool   `json:"accepted"`
	Done        bool   `json:"done"`
	Status      string `json:"status"`
	AlreadyDone bool   `json:"already_done"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	TimedOut    *bool  `json:"timed_out,omitempty"`
	Canceled    *bool  `json:"canceled,omitempty"`
	Error       string `json:"error,omitempty"`
	NextAction  string `json:"next_action,omitempty"`
}

type invitationResult struct {
	ClientToken              string               `json:"client_token"`
	MultiDevice              bool                 `json:"multi_device"`
	Invitation               string               `json:"invitation"`
	ExpiresAt                string               `json:"expires_at"`
	NextAction               invitationNextAction `json:"next_action"`
	MacOSLinuxCommand        string               `json:"macos_linux_command"`
	WindowsPowerShellCommand string               `json:"windows_powershell_command"`
	DisplayText              string               `json:"display_text"`
}

type invitationNextAction struct {
	Tool         string                  `json:"tool"`
	Arguments    invitationWaitArguments `json:"arguments"`
	Until        string                  `json:"until"`
	Instructions string                  `json:"instructions"`
}

type invitationWaitArguments struct {
	ClientToken string `json:"client_token"`
	WaitMS      int    `json:"wait_ms"`
}

func resultSchema[T any]() *jsonschema.Schema {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		panic(err)
	}
	describeResult(schema)
	return schema
}

func describeResult(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}
	for name, property := range schema.Properties {
		property.Description = serverText("mcp.result." + name)
		if name == "status" && schema.Properties["client_id"] != nil {
			property.Description = serverText("mcp.client.status")
		}
		describeResult(property)
	}
	describeResult(schema.Items)
	for _, definition := range schema.Defs {
		describeResult(definition)
	}
}

func toolDefinition(name, description string, input *jsonschema.Schema) *mcp.Tool {
	var output *jsonschema.Schema
	switch name {
	case "create_client":
		output = resultSchema[invitationResult]()
	case "list_clients":
		output = resultSchema[clientsResult]()
	case "execute_command":
		output = resultSchema[protocol.CommandResult]()
	case "session_start":
		output = resultSchema[sessionStartResult]()
	case "session_read":
		output = resultSchema[sessionReadResult]()
	case "session_write":
		output = resultSchema[sessionWriteResult]()
	case "session_stop":
		output = resultSchema[sessionStopResult]()
		output.Properties["status"].Enum = []any{"stopping", "stopped", "finished"}
		output.Properties["accepted"].Description = serverText("mcp.stop.accepted")
		output.Properties["done"].Description = serverText("mcp.stop.done")
		output.Properties["status"].Description = serverText("mcp.stop.status")
	}
	readOnly := name == "list_clients" || name == "session_read"
	destructive := name == "execute_command" || name == "session_start" || name == "session_write" || name == "session_stop"
	openWorld := name == "execute_command" || name == "session_start" || name == "session_write" || name == "session_stop"
	return &mcp.Tool{
		Name: name, Description: serverText(description), InputSchema: input, OutputSchema: output,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: readOnly, DestructiveHint: &destructive,
			IdempotentHint: readOnly || name == "session_stop", OpenWorldHint: &openWorld,
		},
	}
}
