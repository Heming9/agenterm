package service

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpAuthKey struct{}
type mcpPrincipal struct {
	Admin  bool
	Secret string
	IP     string
}

// Authentication stays outside the SDK so all endpoints share token lifetimes,
// device isolation and the same source-IP limiter.
func (s *frontend) mcp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	principal, err := controlCall[mcpPrincipal](r.Context(), s.control, "authenticate", requestIdentity{Header: r.Header, RemoteAddr: r.RemoteAddr})
	if err != nil {
		writeControlError(w, err)
		return
	}
	s.mcpOnce.Do(func() {
		management, device := s.newMCPServer(true), s.newMCPServer(false)
		handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
			if r.Context().Value(mcpAuthKey{}).(mcpPrincipal).Admin {
				return management
			}
			return device
		}, &mcp.StreamableHTTPOptions{
			Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 1 << 20,
			PropagateRequestCancellation: true,
		})
		s.mcpHandler = http.NewCrossOriginProtection().Handler(handler)
	})
	s.mcpHandler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), mcpAuthKey{}, principal)))
}

type targetArgs struct {
	ClientToken string `json:"client_token,omitempty"`
}

func (a targetArgs) targetToken() string { return a.ClientToken }

type listClientsArgs struct {
	targetArgs
	WaitMS int `json:"wait_ms,omitempty"`
}
type executeArgs struct {
	targetArgs
	ClientID string `json:"client_id,omitempty"`
	Command  string `json:"command"`
	protocol.ExecOptions
}
type readSessionArgs struct {
	targetArgs
	SessionID string `json:"session_id"`
	Cursor    int64  `json:"cursor,omitempty"`
	WaitMS    int    `json:"wait_ms,omitempty"`
	MaxBytes  *int   `json:"max_bytes,omitempty"`
}
type listSessionsArgs struct {
	targetArgs
	ClientID string `json:"client_id,omitempty"`
}
type controlSessionArgs struct {
	targetArgs
	SessionID string `json:"session_id"`
}

// Session actions share a target, but have different argument/result contracts.
// Pointers preserve presence so unrelated arguments can be rejected.
type sessionActionArgs struct {
	targetArgs
	SessionID string  `json:"session_id"`
	Action    string  `json:"action"`
	Input     *string `json:"input,omitempty"`
	WaitMS    *int    `json:"wait_ms,omitempty"`
}
type writeSessionArgs struct {
	targetArgs
	SessionID string `json:"session_id"`
	Input     string `json:"input"`
}
type stopSessionArgs struct {
	targetArgs
	SessionID string `json:"session_id"`
	WaitMS    int    `json:"wait_ms,omitempty"`
}
type createClientArgs struct {
	MultiDevice    bool  `json:"multi_device,omitempty"`
	TimeoutSeconds int64 `json:"timeout_seconds,omitempty"`
}

func addTargetTool[In interface{ targetToken() string }, Out any](s *frontend, ms *mcp.Server, admin bool, name, description string, handle func(context.Context, string, In) (*mcp.CallToolResult, Out, error)) {
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(err)
	}
	if admin {
		schema.Required = append(schema.Required, "client_token")
	} else {
		delete(schema.Properties, "client_token")
	}
	descriptions := map[string]string{
		"client_token": "mcp.target.client.connection.token.required.when.authenticated.with",
		"client_id":    "mcp.client.id", "command": "mcp.shell.command",
		"cwd": "mcp.execution.cwd", "env": "mcp.execution.env", "timeout_ms": "mcp.execution.timeout",
		"session_id": "mcp.session.identifier", "cursor": "mcp.byte.cursor.initially.0",
		"wait_ms": "mcp.wait.up.to.this.many.milliseconds.for.output", "input": "mcp.input.bytes.as.text.e.g.yes.n",
		"max_bytes": "mcp.session.max.bytes",
		"action":    "mcp.session.control.action",
	}
	for field, property := range schema.Properties {
		if key := descriptions[field]; key != "" {
			property.Description = serverText(key)
		}
		switch field {
		case "command", "session_id", "client_token":
			property.MinLength = new(1)
		case "client_id":
			property.Pattern = "^[a-z0-9]{6}$"
		case "cursor":
			property.Minimum = new(float64(0))
		case "max_bytes":
			property.Minimum, property.Maximum = new(float64(4)), new(float64(maxSessionReadBytes))
			property.Default = json.RawMessage("16384")
		case "wait_ms":
			property.Minimum, property.Maximum, property.Default = new(float64(0)), new(float64(20000)), json.RawMessage("0")
			if name == "list_clients" {
				property.Maximum = new(float64(300000))
				property.Description = serverText("mcp.wait.for.connection")
			}
			if name == "session_control" {
				// A global default would also inject wait_ms into non-stop actions.
				property.Default = nil
				property.Description = serverText("mcp.stop.wait")
			}
		case "timeout_ms":
			property.Minimum, property.Maximum = new(float64(0)), new(float64(protocol.MaxCommandTimeoutMS))
			property.Default = json.RawMessage("0")
			if name == "execute_command" {
				property.Default, _ = json.Marshal(protocol.DefaultCommandTimeoutMS)
			}
		}
	}
	if name == "session_control" {
		schema.Properties["action"].Enum = []any{"input", "interrupt", "eof", "stop"}
		schema.OneOf = []*jsonschema.Schema{
			{Properties: map[string]*jsonschema.Schema{"action": {Enum: []any{"input"}}}, Required: []string{"input"}, Not: &jsonschema.Schema{Required: []string{"wait_ms"}}},
			{Properties: map[string]*jsonschema.Schema{"action": {Enum: []any{"interrupt", "eof"}}}, Not: &jsonschema.Schema{AnyOf: []*jsonschema.Schema{{Required: []string{"input"}}, {Required: []string{"wait_ms"}}}}},
			{Properties: map[string]*jsonschema.Schema{"action": {Enum: []any{"stop"}}}, Not: &jsonschema.Schema{Required: []string{"input"}}},
		}
	}
	mcp.AddTool(ms, toolDefinition(name, description, schema), func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		principal := ctx.Value(mcpAuthKey{}).(mcpPrincipal)
		secret, err := controlCall[string](ctx, s.control, "target", targetRequest{Principal: principal, Token: in.targetToken()})
		if err != nil {
			return nil, zero, err
		}
		if secret == "" {
			if name == "list_clients" {
				return nil, zero, i18n.NewError("mcp.connection.wait.failed", i18n.NewError("valid.client.token.is.required"))
			}
			return nil, zero, i18n.NewError("valid.client.token.is.required")
		}
		return handle(ctx, secret, in)
	})
}

func validateExecution(a executeArgs) error {
	if strings.TrimSpace(a.Command) == "" {
		return i18n.NewError("command.is.required")
	}
	return a.ExecOptions.Validate()
}

func (a sessionActionArgs) validate() error {
	if a.SessionID == "" {
		return i18n.NewError("session.control.invalid.arguments")
	}
	switch a.Action {
	case "input":
		if a.Input == nil || a.WaitMS != nil {
			return i18n.NewError("session.control.invalid.arguments")
		}
	case "interrupt", "eof":
		if a.Input != nil || a.WaitMS != nil {
			return i18n.NewError("session.control.invalid.arguments")
		}
	case "stop":
		if a.Input != nil || (a.WaitMS != nil && (*a.WaitMS < 0 || *a.WaitMS > 20000)) {
			return i18n.NewError("session.control.invalid.arguments")
		}
	default:
		return i18n.NewError("session.control.invalid.arguments")
	}
	return nil
}

func (s *frontend) sessionControl(ctx context.Context, secret string, a sessionActionArgs) (map[string]any, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	var out map[string]any
	var err error
	switch a.Action {
	case "input":
		out, err = controlCall[map[string]any](ctx, s.control, "session/write", toolRequest[writeSessionArgs]{Secret: secret,
			Args: writeSessionArgs{SessionID: a.SessionID, Input: *a.Input}})
	case "interrupt", "eof":
		out, err = controlCall[map[string]any](ctx, s.control, "session/"+a.Action, toolRequest[controlSessionArgs]{Secret: secret,
			Args: controlSessionArgs{SessionID: a.SessionID}})
	case "stop":
		wait := 1000
		if a.WaitMS != nil {
			wait = *a.WaitMS
		}
		out, err = controlCall[map[string]any](ctx, s.control, "session/stop", toolRequest[stopSessionArgs]{Secret: secret,
			Args: stopSessionArgs{SessionID: a.SessionID, WaitMS: wait}})
	}
	if err != nil {
		return nil, err
	}
	out["action"] = a.Action
	return out, nil
}

func (s *frontend) newMCPServer(admin bool) *mcp.Server {
	ms := mcp.NewServer(&mcp.Implementation{Name: "agenterm", Version: version.Current}, &mcp.ServerOptions{Instructions: serverText("mcp.connection.context") + "\n\n" + serverText("mcp.execution.workflow")})
	ms.AddReceivingMiddleware(observeMCP)
	addTargetTool(s, ms, admin, "p2p_transfer_start", "mcp.p2p.transfer.start", func(ctx context.Context, secret string, a p2pStartArgs) (*mcp.CallToolResult, p2pStartResult, error) {
		out, err := s.p2pTransferStart(ctx, secret, a)
		return nil, out, err
	})
	addTargetTool(s, ms, admin, "list_clients", "mcp.list.clients", func(ctx context.Context, secret string, a listClientsArgs) (*mcp.CallToolResult, map[string]any, error) {
		clients, err := controlCall[[]clientInfo](ctx, s.control, "clients", toolRequest[listClientsArgs]{Secret: secret, Args: a})
		if err != nil {
			return nil, nil, i18n.NewError("mcp.connection.wait.failed", err)
		}
		out := map[string]any{"clients": clients}
		if a.WaitMS > 0 {
			out["status"] = "waiting"
			if len(clients) > 0 {
				out["status"] = "connected"
			}
		}
		return nil, out, nil
	})
	addTargetTool(s, ms, admin, "execute_command", "mcp.execute.a.non.interactive.command.and.return.output", func(ctx context.Context, secret string, a executeArgs) (*mcp.CallToolResult, protocol.CommandResult, error) {
		if err := validateExecution(a); err != nil {
			return nil, protocol.CommandResult{}, err
		}
		out, err := s.execute(ctx, secret, a)
		return &mcp.CallToolResult{IsError: out.Failed()}, out, err
	})
	addTargetTool(s, ms, admin, "session_start", "mcp.start.an.interactive.terminal.session.then.use.session", func(ctx context.Context, secret string, a executeArgs) (*mcp.CallToolResult, map[string]any, error) {
		if err := validateExecution(a); err != nil {
			return nil, nil, err
		}
		out, err := controlCall[sessionStartResult](ctx, s.control, "session/start", toolRequest[executeArgs]{Secret: secret, Args: a})
		return nil, map[string]any{"session_id": out.SessionID, "cursor": out.Cursor}, err
	})
	addTargetTool(s, ms, admin, "session_read", "mcp.read.new.output.while.the.session.is.running", func(ctx context.Context, secret string, a readSessionArgs) (*mcp.CallToolResult, map[string]any, error) {
		out, err := controlCall[map[string]any](ctx, s.control, "session/read", toolRequest[readSessionArgs]{Secret: secret, Args: a})
		return nil, out, err
	})
	addTargetTool(s, ms, admin, "session_list", "mcp.session.list", func(ctx context.Context, secret string, a listSessionsArgs) (*mcp.CallToolResult, sessionListResult, error) {
		out, err := controlCall[sessionListResult](ctx, s.control, "session/list", toolRequest[listSessionsArgs]{Secret: secret, Args: a})
		return nil, out, err
	})
	addTargetTool(s, ms, admin, "session_control", "mcp.session.control", func(ctx context.Context, secret string, a sessionActionArgs) (*mcp.CallToolResult, map[string]any, error) {
		out, err := s.sessionControl(ctx, secret, a)
		return nil, out, err
	})
	if admin {
		input, err := jsonschema.For[createClientArgs](nil)
		if err != nil {
			panic(err)
		}
		input.Properties["multi_device"].Description = serverText("mcp.result.multi_device")
		timeout := input.Properties["timeout_seconds"]
		timeout.Description = serverText("mcp.invitation.timeout.seconds")
		timeout.Minimum = new(float64(1))
		timeout.Maximum = new(float64(maxInvitationTimeoutSeconds))
		timeout.Default, _ = json.Marshal(defaultInvitationTimeoutSeconds)
		mcp.AddTool(ms, toolDefinition("create_client", "mcp.create.a.client.connection.token.and.a.one", input), func(ctx context.Context, _ *mcp.CallToolRequest, a createClientArgs) (*mcp.CallToolResult, map[string]any, error) {
			principal := ctx.Value(mcpAuthKey{}).(mcpPrincipal)
			out, err := controlCall[map[string]any](ctx, s.control, "create", createRequest{IP: principal.IP, Args: a})
			if err != nil {
				return nil, nil, err
			}
			expires, err := time.Parse(time.RFC3339, out["expires_at"].(string))
			if err != nil {
				return nil, nil, err
			}
			timeoutSeconds := a.TimeoutSeconds
			if timeoutSeconds == 0 {
				timeoutSeconds = defaultInvitationTimeoutSeconds
			}
			out["display_text"] = connectionDisplayText(out["client_token"].(string), out["macos_linux_command"].(string), out["windows_powershell_command"].(string), timeoutSeconds, expires, out["multi_device"].(bool))
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out["display_text"].(string)}}}, out, nil
		})
	}
	return ms
}
