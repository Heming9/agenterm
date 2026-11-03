package service

import (
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// These types define the wire contracts, including optional terminal fields.
// Legacy map-based handlers keep their JSON shape and are checked against these
// explicit schemas by the MCP SDK.
type clientsResult struct {
	Clients []clientInfo `json:"clients"`
	Status  string       `json:"status,omitempty"`
}

type executeResult struct {
	sessionReadResult
	SessionCreated bool `json:"session_created"`
}

type sessionReadResult struct {
	Persistent   *bool  `json:"persistent,omitempty"`
	Promoted     *bool  `json:"promoted,omitempty"`
	SessionAlive *bool  `json:"session_alive,omitempty"`
	CommandDone  *bool  `json:"command_done,omitempty"`
	CommandID    string `json:"command_id,omitempty"`
	Status       string `json:"status,omitempty"`
	SessionID    string `json:"session_id"`
	Output       string `json:"output"`
	NextCursor   int64  `json:"next_cursor"`
	Truncated    bool   `json:"truncated"`
	HasMore      bool   `json:"has_more"`
	Done         bool   `json:"done"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	TimedOut     *bool  `json:"timed_out,omitempty"`
	Canceled     *bool  `json:"canceled,omitempty"`
	Error        string `json:"error,omitempty"`
}

type sessionWriteResult struct {
	Accepted bool `json:"accepted"`
}

type sessionListResult struct {
	Sessions []sessionInfo `json:"sessions"`
}

type sessionInfo struct {
	Persistent        bool       `json:"persistent"`
	Promoted          bool       `json:"promoted"`
	CommandID         string     `json:"command_id,omitempty"`
	CommandDone       bool       `json:"command_done"`
	SessionAlive      bool       `json:"session_alive"`
	SessionID         string     `json:"session_id"`
	ClientID          string     `json:"client_id"`
	Command           string     `json:"command"`
	StartedAt         time.Time  `json:"started_at"`
	EndedAt           *time.Time `json:"ended_at,omitempty"`
	Status            string     `json:"status"`
	Done              bool       `json:"done"`
	OutputStartCursor int64      `json:"output_start_cursor"`
	OutputEndCursor   int64      `json:"output_end_cursor"`
	ExitCode          *int       `json:"exit_code,omitempty"`
	Error             string     `json:"error,omitempty"`
}

type sessionControlResult struct {
	Delivered bool `json:"delivered"`
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
}

type invitationResult struct {
	ClientToken              string `json:"client_token"`
	TokenIdentifier          string `json:"token_identifier,omitempty"`
	MultiDevice              bool   `json:"multi_device"`
	Invitation               string `json:"invitation"`
	ExpiresAt                string `json:"expires_at"`
	MacOSLinuxCommand        string `json:"macos_linux_command"`
	WindowsPowerShellCommand string `json:"windows_powershell_command"`
	DisplayText              string `json:"display_text"`
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
		if name == "status" && schema.Properties["command_done"] != nil && schema.Properties["client_id"] == nil {
			property.Description = serverText("mcp.execution.status")
			property.Enum = []any{"running", "completed"}
		}
		if name == "status" && schema.Properties["client_id"] != nil {
			property.Description = serverText("mcp.client.status")
			if schema.Properties["session_id"] != nil {
				property.Description = serverText("mcp.session.status")
				property.Enum = []any{"idle", "running", "stopping", "finished"}
			}
		}
		describeResult(property)
	}
	describeResult(schema.Items)
	for _, definition := range schema.Defs {
		describeResult(definition)
	}
}

func sessionActionResultSchema() *jsonschema.Schema {
	// Each action has a distinct closed result shape. In particular an input
	// acknowledgment cannot be confused with delivery or confirmed termination.
	output := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{
		"action": {Type: "string", Enum: []any{"input", "interrupt", "eof", "stop"}, Description: serverText("mcp.result.action")},
	}, Required: []string{"action"}}
	for _, action := range []string{"input", "interrupt", "eof", "stop"} {
		var branch *jsonschema.Schema
		switch action {
		case "input":
			branch = resultSchema[sessionWriteResult]()
		case "interrupt", "eof":
			branch = resultSchema[sessionControlResult]()
		case "stop":
			branch = resultSchema[sessionStopResult]()
			branch.Properties["status"].Enum = []any{"stopping", "stopped", "finished"}
			branch.Properties["accepted"].Description = serverText("mcp.stop.accepted")
			branch.Properties["done"].Description = serverText("mcp.stop.done")
			branch.Properties["status"].Description = serverText("mcp.stop.status")
		}
		branch.Properties["action"] = &jsonschema.Schema{Type: "string", Enum: []any{action}, Description: serverText("mcp.result.action")}
		branch.Required = append(branch.Required, "action")
		branch.AdditionalProperties = &jsonschema.Schema{Not: &jsonschema.Schema{}}
		output.OneOf = append(output.OneOf, branch)
	}
	return output
}

func toolDefinition(name, description string, input *jsonschema.Schema) *mcp.Tool {
	var output *jsonschema.Schema
	switch name {
	case "create_client":
		output = resultSchema[invitationResult]()
	case "list_clients":
		output = resultSchema[clientsResult]()
	case "execute_command":
		output = resultSchema[executeResult]()
	case "session_read":
		output = resultSchema[sessionReadResult]()
	case "session_list":
		output = resultSchema[sessionListResult]()
	case "session_control":
		output = sessionActionResultSchema()
	case "p2p_transfer_start":
		output = resultSchema[p2pStartResult]()
	}
	readOnly := name == "list_clients" || name == "session_read" || name == "session_list"
	destructive := name == "execute_command" || name == "session_control" || name == "p2p_transfer_start"
	openWorld := destructive
	return &mcp.Tool{
		Name: name, Description: serverText(description), InputSchema: input, OutputSchema: output,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: readOnly, DestructiveHint: &destructive,
			IdempotentHint: readOnly, OpenWorldHint: &openWorld,
		},
	}
}
