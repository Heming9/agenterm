package service

import (
	terminal "agenterm/internal/client"
	"agenterm/internal/i18n"
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

// Authentication stays outside the SDK to enforce token lifetimes and device isolation.
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

// Legacy requests may still ask for 300 seconds. Bound each HTTP response;
// agents perform separate polls within their overall connection-wait budget.
const maxConnectionPollMS = 30000

type closeClientArgs struct {
	targetArgs
	ClientIDs []string `json:"client_ids,omitempty"`
}
type executeArgs struct {
	targetArgs
	ClientID string `json:"client_id,omitempty"`
	terminal.ExecuteRequest
}
type readSessionArgs struct {
	targetArgs
	ClientID string `json:"client_id,omitempty"`
	terminal.ReadRequest
}
type listSessionsArgs struct {
	targetArgs
	ClientID string `json:"client_id,omitempty"`
	Status   string `json:"status,omitempty"`
}

type sessionActionArgs struct {
	targetArgs
	ClientID string `json:"client_id,omitempty"`
	terminal.ControlRequest
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
		"client_ids": "mcp.client.ids",
		"setup":      "mcp.execution.setup",
		"session_id": "mcp.session.identifier", "cursor": "mcp.byte.cursor.initially.0",
		"wait_ms": "mcp.wait.up.to.this.many.milliseconds.for.output", "input": "mcp.input.bytes.as.text.e.g.yes.n",
		"max_bytes":  "mcp.session.max.bytes",
		"command_id": "mcp.execution.command.id",
		"action":     "mcp.session.control.action",
		"status":     "mcp.session.list.status",
	}
	for field, property := range schema.Properties {
		if key := descriptions[field]; key != "" {
			property.Description = serverText(key)
		}
		switch field {
		case "command", "session_id", "client_token":
			property.MinLength = new(1)
			if field == "session_id" {
				property.MaxLength = new(64)
			}
		case "client_id":
			property.Pattern = "^[a-z0-9]{6}$"
		case "client_ids":
			property.MinItems = new(1)
			property.UniqueItems = true
			property.Items.Pattern = "^[a-z0-9]{6}$"
		case "cursor":
			property.Minimum = new(float64(0))
		case "status":
			property.Enum = []any{"all", "live", "running", "idle", "closed"}
		case "max_bytes":
			property.Minimum, property.Maximum = new(float64(terminal.MinReadBytes)), new(float64(terminal.MaxReadBytes))
			property.Default, _ = json.Marshal(terminal.DefaultReadBytes)
		case "wait_ms":
			property.Minimum, property.Maximum, property.Default = new(float64(0)), new(float64(terminal.MaxWaitMS)), json.RawMessage("0")
			if name == "list_client" {
				property.Maximum = new(float64(300000))
				property.Description = serverText("mcp.wait.for.connection")
			}
			if name == "execute_command" {
				property.Default, _ = json.Marshal(terminal.DefaultWaitMS)
				property.Description = serverText("mcp.execution.wait")
			}
			if name == "session_control" {
				// A global default would also inject wait_ms into non-stop actions.
				property.Default = nil
				property.Description = serverText("mcp.stop.wait")
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
	if name == "execute_command" {
		schema.Properties["session_id"].Description = serverText("mcp.execution.session.id")
		schema.Not = &jsonschema.Schema{Required: []string{"session_id", "setup"}}
		setup := schema.Properties["setup"]
		// Resolve pointer schemas by their generated definition when necessary.
		if setup.Ref != "" {
			setup = schema.Defs[strings.TrimPrefix(setup.Ref, "#/$defs/")]
		}
		for field, property := range setup.Properties {
			key := "mcp.execution." + field
			if field == "session_id" {
				key = "mcp.execution.setup.session.id"
				property.MinLength, property.MaxLength = new(1), new(64)
			}
			property.Description = serverText(key)
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
			if name == "list_client" {
				return nil, zero, i18n.NewError("mcp.connection.wait.failed", i18n.NewError("valid.client.token.is.required"))
			}
			return nil, zero, i18n.NewError("valid.client.token.is.required")
		}
		return handle(ctx, secret, in)
	})
}

func (s *frontend) newMCPServer(admin bool) *mcp.Server {
	ms := mcp.NewServer(&mcp.Implementation{Name: "agenterm", Version: version.Current}, &mcp.ServerOptions{Instructions: serverText("mcp.connection.context")})
	ms.AddReceivingMiddleware(observeMCP)
	addTargetTool(s, ms, admin, "p2p_transfer_start", "mcp.p2p.transfer.start", func(ctx context.Context, secret string, a p2pStartArgs) (*mcp.CallToolResult, p2pStartResult, error) {
		out, err := s.p2pTransferStart(ctx, secret, a)
		return nil, out, err
	})
	addTargetTool(s, ms, admin, "list_client", "mcp.list.clients", func(ctx context.Context, secret string, a listClientsArgs) (*mcp.CallToolResult, map[string]any, error) {
		a.WaitMS = min(a.WaitMS, maxConnectionPollMS)
		clients, err := controlCall[[]clientInfo](ctx, s.control, "clients", toolRequest[listClientsArgs]{Secret: secret, Args: a})
		if err != nil {
			return nil, nil, i18n.NewError("mcp.connection.wait.failed", err)
		}
		return nil, map[string]any{"clients": clients}, nil
	})
	addTargetTool(s, ms, admin, "close_client", "mcp.close.client", func(ctx context.Context, secret string, a closeClientArgs) (*mcp.CallToolResult, closeClientResult, error) {
		out, err := controlCall[closeClientResult](ctx, s.control, "clients/close", toolRequest[closeClientArgs]{Secret: secret, Args: a})
		return nil, out, err
	})
	addTargetTool(s, ms, admin, "execute_command", "mcp.execution.workflow", func(ctx context.Context, secret string, a executeArgs) (*mcp.CallToolResult, map[string]any, error) {
		out, err := controlCall[map[string]any](ctx, s.control, "terminal/execute", toolRequest[executeArgs]{Secret: secret, Args: a})
		failed := out != nil && (out["error"] != nil || (out["exit_code"] != nil && out["exit_code"] != float64(0)))
		return &mcp.CallToolResult{IsError: failed}, out, err
	})
	addTargetTool(s, ms, admin, "session_read", "mcp.read.new.output.while.the.session.is.running", func(ctx context.Context, secret string, a readSessionArgs) (*mcp.CallToolResult, map[string]any, error) {
		out, err := controlCall[map[string]any](ctx, s.control, "terminal/read", toolRequest[readSessionArgs]{Secret: secret, Args: a})
		return nil, out, err
	})
	addTargetTool(s, ms, admin, "session_list", "mcp.session.list", func(ctx context.Context, secret string, a listSessionsArgs) (*mcp.CallToolResult, sessionListResult, error) {
		out, err := controlCall[sessionListResult](ctx, s.control, "terminal/list", toolRequest[listSessionsArgs]{Secret: secret, Args: a})
		if err == nil {
			// Presentation stays in the frontend, including with a retained older
			// runtime or client. Session states still come solely from the client.
			sessions := make([]sessionInfo, 0, len(out.Sessions))
			for _, info := range out.Sessions {
				switch a.Status {
				case "", "all":
				case "live":
					if info.Status != "running" && info.Status != "idle" {
						continue
					}
				default:
					if info.Status != a.Status {
						continue
					}
				}
				info.Summarize()
				sessions = append(sessions, info)
			}
			out.Sessions = sessions
		}
		return nil, out, err
	})
	addTargetTool(s, ms, admin, "session_control", "mcp.session.control", func(ctx context.Context, secret string, a sessionActionArgs) (*mcp.CallToolResult, map[string]any, error) {
		out, err := controlCall[map[string]any](ctx, s.control, "terminal/control", toolRequest[sessionActionArgs]{Secret: secret, Args: a})
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
			var codeExpires time.Time
			code, _ := out["short_code"].(string)
			if code != "" {
				out["short_code_url"] = shortCodeURL(out["client_token"].(string), code)
				codeExpires, err = time.Parse(time.RFC3339, out["short_code_expires_at"].(string))
				if err != nil {
					return nil, nil, err
				}
			}
			tokenExpires, err := time.Parse(time.RFC3339, out["token_initial_expires_at"].(string))
			if err != nil {
				return nil, nil, err
			}
			shell, powershell := installCommands(out["client_token"].(string))
			out["macos_linux_command"] = shell
			out["windows_powershell_command"] = powershell
			out["display_text"] = connectionDisplayText(out["client_token"].(string), out["macos_linux_command"].(string), out["windows_powershell_command"].(string), code, timeoutSeconds, expires, codeExpires, tokenExpires, out["multi_device"].(bool))
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out["display_text"].(string)}}}, out, nil
		})
	}
	return ms
}
