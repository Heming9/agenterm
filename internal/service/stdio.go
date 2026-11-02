package service

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sync/errgroup"
)

// The process owner is the management principal on stdio. HTTP retains its own
// authentication and serves the invitations, installers, and client WebSockets.
func (s *frontend) runStdio(ctx context.Context, srv *http.Server, listener net.Listener, transport mcp.Transport, ready ...func() error) error {
	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		err := srv.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
	group.Go(func() error {
		defer func() { s.stopping.Store(true); _ = srv.Close() }()
		if len(ready) > 0 {
			if err := ready[0](); err != nil {
				return err
			}
		}
		ctx := context.WithValue(ctx, mcpAuthKey{}, mcpPrincipal{Admin: true, IP: "127.0.0.1"})
		return s.newMCPServer(true).Run(ctx, transport)
	})
	return group.Wait()
}
