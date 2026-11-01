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
}
type writeSessionArgs struct {
	targetArgs
	SessionID string `json:"session_id"`
	Input     string `json:"input"`
}
type stopSessionArgs struct {
	targetArgs
	SessionID string `json:"session_id"`
}
type createClientArgs struct {
	MultiDevice bool `json:"multi_device,omitempty"`
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
		case "wait_ms":
			property.Minimum, property.Maximum, property.Default = new(float64(0)), new(float64(20000)), json.RawMessage("0")
			if name == "list_clients" {
				property.Description = serverText("mcp.wait.for.connection")
			}
		case "timeout_ms":
			property.Minimum, property.Maximum = new(float64(0)), new(float64(protocol.MaxCommandTimeoutMS))
			property.Default = json.RawMessage("0")
			if name == "execute_command" {
				property.Default, _ = json.Marshal(protocol.DefaultCommandTimeoutMS)
			}
		}
	}
	mcp.AddTool(ms, &mcp.Tool{Name: name, Description: serverText(description), InputSchema: schema}, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
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

func (s *server) newMCPServer(admin bool) *mcp.Server {
	ms := mcp.NewServer(&mcp.Implementation{Name: "agenterm", Version: version.Current}, &mcp.ServerOptions{Instructions: serverText("mcp.connection.workflow") + "\n\n" + serverText("mcp.execution.workflow")})
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
		out, err := p.read(ctx, a.Cursor, time.Duration(a.WaitMS)*time.Millisecond)
		return nil, out, err
	})
	addTargetTool(s, ms, admin, "session_write", "mcp.send.input.to.a.running.session.include.a", func(_ context.Context, secret string, a writeSessionArgs) (*mcp.CallToolResult, map[string]any, error) {
		p, err := s.getSession(secret, a.SessionID)
		if err != nil {
			return nil, nil, err
		}
		err = p.send("input", a.Input)
		return nil, map[string]any{"accepted": err == nil}, err
	})
	addTargetTool(s, ms, admin, "session_stop", "mcp.stop.a.running.session", func(_ context.Context, secret string, a stopSessionArgs) (*mcp.CallToolResult, map[string]any, error) {
		p, err := s.getSession(secret, a.SessionID)
		if err != nil {
			return nil, nil, err
		}
		err = p.send("cancel", "")
		return nil, map[string]any{"accepted": err == nil}, err
	})
	if admin {
		mcp.AddTool(ms, &mcp.Tool{Name: "create_client", Description: serverText("mcp.create.a.client.connection.token.and.a.one")}, func(ctx context.Context, _ *mcp.CallToolRequest, a createClientArgs) (*mcp.CallToolResult, map[string]any, error) {
			principal := ctx.Value(mcpAuthKey{}).(mcpPrincipal)
			out, err := s.createClientMode(principal.ip, a.MultiDevice)
			return nil, out, err
		})
	}
	return ms
}
