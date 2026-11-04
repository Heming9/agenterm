package service

import (
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// These types define the wire contracts, including optional terminal fields.
// Legacy map-based handlers keep their JSON shape and are checked against these
// explicit schemas by the MCP SDK.
type clientsResult struct {
	Clients []clientInfo `json:"clients"`
}
type closeClientResult struct {
	ClientIDs []string `json:"client_ids"`
}

type executeResult = sessionReadResult

// Execution and incremental reads share six regular fields. Completion and
// exceptional output conditions add only the fields needed by the caller.
type sessionReadResult struct {
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

type sessionListResult struct {
	Sessions []sessionInfo `json:"sessions"`
}

type sessionInfo struct {
	SessionID string `json:"session_id"`
	ClientID  string `json:"client_id"`
	Command   string `json:"command"`
	CommandID string `json:"command_id"`
	Status    string `json:"status"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Error     string `json:"error,omitempty"`
}

type sessionControlResult struct {
	OK bool `json:"ok"`
}

type sessionStopResult struct {
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
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
		if name == "truncated" && schema.Properties["next_cursor"] != nil {
			property.Enum = []any{true}
		}
		if name == "status" && schema.Properties["next_cursor"] != nil {
			property.Description = serverText("mcp.execution.status")
			property.Enum = []any{"running", "idle", "closed"}
		}
		if name == "status" && schema.Properties["client_id"] != nil && schema.Properties["session_id"] != nil {
			property.Description = serverText("mcp.execution.status")
			property.Enum = []any{"running", "idle", "closed"}
		}
		describeResult(property)
	}
	describeResult(schema.Items)
	for _, definition := range schema.Defs {
		describeResult(definition)
	}
}

func sessionActionResultSchema() *jsonschema.Schema {
	acknowledgment := resultSchema[sessionControlResult]()
	acknowledgment.Properties["ok"].Enum = []any{true}
	stopped := resultSchema[sessionStopResult]()
	stopped.Properties["status"].Enum = []any{"running", "closed"}
	stopped.Properties["status"].Description = serverText("mcp.stop.status")
	for _, branch := range []*jsonschema.Schema{acknowledgment, stopped} {
		branch.AdditionalProperties = &jsonschema.Schema{Not: &jsonschema.Schema{}}
	}
	return &jsonschema.Schema{Type: "object", OneOf: []*jsonschema.Schema{acknowledgment, stopped}}
}

func toolDefinition(name, description string, input *jsonschema.Schema) *mcp.Tool {
	var output *jsonschema.Schema
	switch name {
	case "create_client":
		output = resultSchema[invitationResult]()
	case "list_client":
		output = resultSchema[clientsResult]()
	case "close_client":
		output = resultSchema[closeClientResult]()
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
	readOnly := name == "list_client" || name == "session_read" || name == "session_list"
	destructive := name == "execute_command" || name == "session_control" || name == "p2p_transfer_start" || name == "close_client"
	openWorld := destructive
	return &mcp.Tool{
		Name: name, Description: serverText(description), InputSchema: input, OutputSchema: output,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: readOnly, DestructiveHint: &destructive,
			IdempotentHint: readOnly, OpenWorldHint: &openWorld,
		},
	}
}
