package service

import (
	"agenterm/internal/invite"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStdioSharesInvitationsWithAuthenticatedHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + listener.Addr().String()
	s := &runtimeState{adminKey: "http-management-key", clientURL: "ws://" + listener.Addr().String() + "/agenterm/client", maxClientsPerIP: 1}
	httpServer := &http.Server{Handler: s.routes(downloadTag, nil)}
	serverInput, clientOutput := io.Pipe()
	clientInput, serverOutput := io.Pipe()
	ended := make(chan error, 1)
	go func() {
		ended <- s.runStdio(ctx, httpServer, listener, &mcp.IOTransport{Reader: serverInput, Writer: serverOutput})
	}()
	t.Cleanup(func() {
		cancel()
		httpServer.Close()
		serverInput.Close()
		serverOutput.Close()
		clientInput.Close()
		clientOutput.Close()
	})
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: clientInput, Writer: clientOutput}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 8 {
		t.Fatalf("tools: %v %v", tools, err)
	}
	created, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "create_client", Arguments: map[string]any{}})
	if err != nil || created.IsError {
		t.Fatalf("create: %v %v", created, err)
	}
	var invitation struct {
		Invitation  string `json:"invitation"`
		ClientToken string `json:"client_token"`
		DisplayText string `json:"display_text"`
	}
	data, err := json.Marshal(created.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &invitation); err != nil {
		t.Fatal(err)
	}
	if len(created.Content) != 1 || invitation.DisplayText == "" || created.Content[0].(*mcp.TextContent).Text != invitation.DisplayText {
		t.Fatal("stdio connection instructions must match structured display_text")
	}
	token, err := invite.Redeem(ctx, invitation.Invitation)
	if err != nil || token != invitation.ClientToken || token == "" {
		t.Fatalf("HTTP invitation redemption: token match=%v err=%v", token == invitation.ClientToken, err)
	}
	listed, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_clients", Arguments: map[string]any{"client_token": token}})
	if err != nil || listed.IsError {
		t.Fatalf("stdio target credentials: %v %v", listed, err)
	}
	response, err := http.Get(origin + "/agenterm/mcp")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stdio must not disable HTTP authentication: %d", response.StatusCode)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ended:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("stdio EOF did not stop the HTTP server")
	}
	if response, err := http.Get(origin + "/agenterm/mcp"); err == nil {
		response.Body.Close()
		t.Fatal("HTTP listener remained open after stdio closed")
	}
}

func TestStdioCancellationStopsBothTransports(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &runtimeState{adminKey: "key"}
	httpServer := &http.Server{Handler: s.routes(downloadTag, nil)}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ended := make(chan error, 1)
	go func() { ended <- s.runStdio(ctx, httpServer, listener, serverTransport) }()
	connectCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-cancel-test", Version: "1"}, nil)
	session, err := client.Connect(connectCtx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	cancel()
	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stdio server did not stop on cancellation")
	}
}
