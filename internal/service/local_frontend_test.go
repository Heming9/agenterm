package service

// Existing contract tests exercise the same private API in memory. Deployment
// and restart tests additionally use real Unix sockets and independent processes.
import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type localControl struct{ state *runtimeState }

func (c localControl) call(ctx context.Context, method string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/"+method, bytes.NewReader(body)).WithContext(ctx)
	r.Header.Set(controlVersionHeader, controlVersion)
	w := httptest.NewRecorder()
	c.state.controlHandler(context.Background()).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		var ce controlError
		if err := json.Unmarshal(w.Body.Bytes(), &ce); err != nil {
			return err
		}
		return &ce
	}
	return json.Unmarshal(w.Body.Bytes(), out)
}

func (s *runtimeState) testFrontend() *frontend {
	return &frontend{control: localControl{s}, clientURL: s.clientURL}
}

func (s *runtimeState) mcp(w http.ResponseWriter, r *http.Request) { s.testFrontend().mcp(w, r) }
func (s *runtimeState) installWrapper(tag string) http.Handler {
	return s.testFrontend().installWrapper(tag)
}
func (s *runtimeState) routes(tag string, assets fs.FS) http.Handler {
	front := s.testFrontend()
	business := front.routes(tag, assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, pathPrefixForTest(s.clientURL)+"/client/") {
			s.connect(w, r)
			return
		}
		switch r.URL.Path {
		case pathPrefixForTest(s.clientURL) + "/client":
			s.connect(w, r)
		case pathPrefixForTest(s.clientURL) + "/invite/redeem":
			s.redeemInvitation(w, r)
		default:
			business.ServeHTTP(w, r)
		}
	})
}

func pathPrefixForTest(clientURL string) string {
	if clientURL == "" {
		return "/agenterm"
	}
	return pathPrefix(clientURL)
}

func (s *runtimeState) runStdio(ctx context.Context, srv *http.Server, listener net.Listener, transport mcp.Transport) error {
	return s.testFrontend().runStdio(ctx, srv, listener, transport)
}
