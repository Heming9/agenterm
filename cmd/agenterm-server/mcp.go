package main

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpAuthKey struct{}
type mcpPrincipal struct {
	admin  bool
	secret string
	ip     string
}

// Authentication stays outside the SDK so all endpoints share token lifetimes,
// device isolation and the same source-IP limiter.
func (s *server) mcp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	header := r.Header.Get("Authorization")
	token := strings.TrimPrefix(header, "Bearer ")
	s.mu.Lock()
	secret, ok := s.tokens[token]
	s.mu.Unlock()
	admin := (s.noPassword && header == "") || (s.adminKey != "" && header == "Bearer "+token && subtle.ConstantTimeCompare([]byte(token), []byte(s.adminKey)) == 1)
	if status, err := s.authorize(r, admin || (header == "Bearer "+token && ok)); err != nil {
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "300")
		} else if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Bearer realm="agenterm"`)
		}
		http.Error(w, err.Error(), status)
		return
	}
	ip, _ := s.requestIP(r) // authorize already validated the source.
	principal := mcpPrincipal{admin: admin, secret: secret, ip: ip}
	s.mcpOnce.Do(func() {
		management, device := s.newMCPServer(true), s.newMCPServer(false)
		handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
			if r.Context().Value(mcpAuthKey{}).(mcpPrincipal).admin {
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

// Session actions share a target, but have different argument/result contracts.
// Pointers preserve presence so unrelated arguments can be rejected.
type sessionActionArgs struct {
	targetArgs
	SessionID string  `json:"session_id"`
	Action    string  `json:"action"`
	Input     *string `json:"input,omitempty"`
	WaitMS    *int    `json:"wait_ms,omitempty"`
}
type createClientArgs struct {
	MultiDevice    bool  `json:"multi_device,omitempty"`
	TimeoutSeconds int64 `json:"timeout_seconds,omitempty"`
}

func addTargetTool[In interface{ targetToken() string }, Out any](s *server, ms *mcp.Server, admin bool, name, description string, handle func(context.Context, string, In) (*mcp.CallToolResult, Out, error)) {
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
		secret := principal.secret
		s.mu.Lock()
		if principal.admin {
			secret = s.tokens[in.targetToken()]
		} else if !s.validSecretLocked(secret) {
			secret = ""
		}
		s.mu.Unlock()
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

func (s *server) sessionControl(ctx context.Context, secret string, a sessionActionArgs) (map[string]any, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	p, err := s.getSession(secret, a.SessionID)
	if err != nil {
		return nil, err
	}
	var result any
	switch a.Action {
	case "input":
		err = p.send("input", *a.Input)
		result = sessionWriteResult{Accepted: err == nil}
	case "interrupt", "eof":
		result, err = p.control(ctx, a.Action)
	case "stop":
		wait := 1000
		if a.WaitMS != nil {
			wait = *a.WaitMS
		}
		result, err = p.stop(ctx, time.Duration(wait)*time.Millisecond)
	}
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	out["action"] = a.Action
	return out, nil
}

func (s *server) newMCPServer(admin bool) *mcp.Server {
	ms := mcp.NewServer(&mcp.Implementation{Name: "agenterm", Version: version.Current}, &mcp.ServerOptions{Instructions: serverText("mcp.connection.context") + "\n\n" + serverText("mcp.execution.workflow")})
	addTargetTool(s, ms, admin, "list_clients", "mcp.list.clients", func(ctx context.Context, secret string, a listClientsArgs) (*mcp.CallToolResult, map[string]any, error) {
		clients, err := s.waitForClients(ctx, secret, time.Duration(a.WaitMS)*time.Millisecond)
		if err != nil {
			return nil, nil, i18n.NewError("mcp.connection.wait.failed", err)
		}
		out := map[string]any{"clients": clients}
		if a.WaitMS > 0 {
			out["status"], out["next_action"] = "waiting", serverText("mcp.connection.keep.waiting")
			if len(clients) > 0 {
				out["status"], out["next_action"] = "connected", serverText("mcp.connection.notify.user")
			}
		}
		return nil, out, nil
	})
	addTargetTool(s, ms, admin, "execute_command", "mcp.execute.a.non.interactive.command.and.return.output", func(ctx context.Context, secret string, a executeArgs) (*mcp.CallToolResult, protocol.CommandResult, error) {
		if err := validateExecution(a); err != nil {
			return nil, protocol.CommandResult{}, err
		}
		out, err := s.execute(ctx, secret, a.ClientID, a.Command, a.ExecOptions)
		return &mcp.CallToolResult{IsError: out.Failed()}, out, err
	})
	addTargetTool(s, ms, admin, "session_start", "mcp.start.an.interactive.terminal.session.then.use.session", func(_ context.Context, secret string, a executeArgs) (*mcp.CallToolResult, map[string]any, error) {
		if err := validateExecution(a); err != nil {
			return nil, nil, err
		}
		p, err := s.startSession(secret, a.ClientID, a.Command, a.ExecOptions)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"session_id": p.id, "cursor": 0}, nil
	})
	addTargetTool(s, ms, admin, "session_read", "mcp.read.new.output.while.the.session.is.running", func(ctx context.Context, secret string, a readSessionArgs) (*mcp.CallToolResult, map[string]any, error) {
		p, err := s.getSession(secret, a.SessionID)
		if err != nil {
			return nil, nil, err
		}
		limit := defaultSessionReadBytes
		if a.MaxBytes != nil {
			limit = *a.MaxBytes
		}
		out, err := p.read(ctx, a.Cursor, time.Duration(a.WaitMS)*time.Millisecond, limit)
		return nil, out, err
	})
	addTargetTool(s, ms, admin, "session_list", "mcp.session.list", func(_ context.Context, secret string, a listSessionsArgs) (*mcp.CallToolResult, sessionListResult, error) {
		return nil, sessionListResult{Sessions: s.listSessions(secret, a.ClientID)}, nil
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
			out, err := s.createClientMode(principal.ip, a.MultiDevice, a.TimeoutSeconds)
			return nil, out, err
		})
	}
	return ms
}
