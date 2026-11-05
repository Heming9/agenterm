package service

import (
	"agenterm/internal/i18n"
	"agenterm/internal/logging"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Version the private API independently of release versions. Additive changes
// remain compatible; incompatible changes require a new API version.
const controlVersion = "12"
const controlVersionHeader = "X-Agenterm-Control-Version"

type control interface {
	call(context.Context, string, any, any) error
}

type runtimeClient struct {
	http *http.Client
}

func unixTransport(socket string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		},
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     30 * time.Second,
	}
}

func newRuntimeClient(socket string) *runtimeClient {
	return &runtimeClient{http: &http.Client{Transport: unixTransport(socket)}}
}

type controlError struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

func (e *controlError) Error() string { return e.Message }

func writeControlError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	var ce *controlError
	if errors.As(err, &ce) {
		status = ce.Status
	}
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "300")
	}
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="agenterm"`)
	}
	http.Error(w, err.Error(), status)
}

func (c *runtimeClient) call(ctx context.Context, method string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://runtime/v1/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(controlVersionHeader, controlVersion)
	if id := logging.RequestID(ctx); id != "" {
		r.Header.Set(logging.RequestIDHeader, id)
	}
	response, err := c.http.Do(r)
	if err != nil {
		return fmt.Errorf("runtime unavailable: %w", err)
	}
	defer response.Body.Close()
	identity := identityFromHeaders(response.Header)
	if response.StatusCode == http.StatusUpgradeRequired ||
		(identity.APIVersion != "" && identity.APIVersion != controlVersion) ||
		(response.StatusCode == http.StatusOK && identity.APIVersion != controlVersion) {
		_ = response.Body.Close()
		return c.incompatibleRuntime(ctx, identity)
	}
	if response.StatusCode != http.StatusOK {
		var ce controlError
		if err := json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&ce); err != nil {
			return fmt.Errorf("runtime returned HTTP %d", response.StatusCode)
		}
		return &ce
	}
	return json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(out)
}

func controlCall[T any](ctx context.Context, c control, method string, in any) (T, error) {
	var out T
	err := c.call(ctx, method, in, &out)
	return out, err
}

type requestIdentity struct {
	Header     http.Header
	RemoteAddr string
}

func (i requestIdentity) request() *http.Request {
	return &http.Request{Header: i.Header, RemoteAddr: i.RemoteAddr}
}

type runtimeConfig struct {
	ClientURL string
	PID       int
}

type targetRequest struct {
	Principal mcpPrincipal
	Token     string
}

type toolRequest[T any] struct {
	Secret string
	Args   T
}

type createRequest struct {
	IP   string
	Args createClientArgs
}

type invitationRequest struct {
	Identity requestIdentity
	Secret   string
	Consume  bool
}

type installerInvitation struct {
	Token       string
	Secret      string
	DeviceToken bool
}

func registerControl[I, O any](mux *http.ServeMux, method string, f func(context.Context, I) (O, error)) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		setRuntimeIdentityHeaders(w.Header())
		fail := func(status int, message string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(controlError{Status: status, Message: message})
		}
		if r.Header.Get(controlVersionHeader) != controlVersion {
			fail(http.StatusUpgradeRequired, "incompatible runtime API version")
			return
		}
		var in I
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
		if err := decoder.Decode(&in); err != nil {
			fail(http.StatusBadRequest, "invalid runtime request")
			return
		}
		out, err := f(r.Context(), in)
		if err != nil {
			status := http.StatusBadRequest
			var ce *controlError
			if errors.As(err, &ce) {
				status = ce.Status
			}
			fail(status, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.Handle("POST /v1/"+method, observeHTTP(handler, true, func(*http.Request) string { return "/v1/" + method }))
}

// Only the runtime constructs this handler. It is reachable exclusively over a
// private, owner-only Unix socket; principals from server are trusted local RPC.
func (s *runtimeState) controlHandler(shutdown ...context.CancelFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+runtimeInfoPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(currentRuntimeIdentity())
	})
	registerControl(mux, "config", func(context.Context, struct{}) (runtimeConfig, error) {
		return runtimeConfig{ClientURL: s.clientURL, PID: os.Getpid()}, nil
	})
	registerControl(mux, "runtime/check", s.checkRuntimeSettings)
	registerControl(mux, "runtime/shutdown", func(context.Context, struct{}) (bool, error) {
		if len(shutdown) == 0 {
			return false, errors.New("runtime shutdown is unavailable")
		}
		shutdown[0]()
		return true, nil
	})
	registerControl(mux, "authenticate", func(_ context.Context, identity requestIdentity) (mcpPrincipal, error) {
		r := identity.request()
		header := r.Header.Get("Authorization")
		token := strings.TrimPrefix(header, "Bearer ")
		s.mu.Lock()
		secret, ok := s.controlSecretLocked(token)
		s.mu.Unlock()
		admin := (s.noPassword && header == "") || (s.adminKey != "" && header == "Bearer "+token && subtle.ConstantTimeCompare([]byte(token), []byte(s.adminKey)) == 1)
		if status, err := s.authorize(r, admin || (header == "Bearer "+token && ok)); err != nil {
			return mcpPrincipal{}, &controlError{Status: status, Message: err.Error()}
		}
		ip, _ := s.requestIP(r)
		return mcpPrincipal{Admin: admin, Secret: secret, IP: ip}, nil
	})
	registerControl(mux, "target", func(_ context.Context, in targetRequest) (string, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if in.Principal.Admin {
			secret, ok := s.controlSecretLocked(in.Token)
			if ok {
				return secret, nil
			}
			return "", nil
		}
		if s.validSecretLocked(in.Principal.Secret) {
			return in.Principal.Secret, nil
		}
		return "", nil
	})
	registerControl(mux, "create", func(_ context.Context, in createRequest) (map[string]any, error) {
		return s.createClientMode(in.IP, in.Args.MultiDevice, in.Args.TimeoutSeconds)
	})
	registerControl(mux, "clients", func(ctx context.Context, in toolRequest[listClientsArgs]) ([]clientInfo, error) {
		return s.waitForClients(ctx, in.Secret, time.Duration(in.Args.WaitMS)*time.Millisecond)
	})
	registerControl(mux, "clients/close", func(ctx context.Context, in toolRequest[closeClientArgs]) (closeClientResult, error) {
		return s.closeClients(ctx, in.Secret, in.Args.ClientIDs)
	})
	registerControl(mux, "p2p/start", s.startP2P)
	registerControl(mux, "p2p/signal", s.p2pSignal)
	registerControl(mux, "terminal/execute", s.executeCommand)
	registerControl(mux, "terminal/read", s.readSession)
	registerControl(mux, "terminal/list", s.listSessions)
	registerControl(mux, "terminal/control", s.controlSession)
	registerControl(mux, "invitation", func(_ context.Context, in invitationRequest) (installerInvitation, error) {
		secret := in.Secret
		s.mu.Lock()
		var entry installerInvitation
		var exists bool
		if len(secret) == shortCodeLength {
			entry.Token, exists = s.shortCodes.lookup(secret, time.Now())
			if exists {
				candidate, ok := s.tokens[entry.Token]
				exists = ok && !s.multiDevice[candidate] && s.tokenUnexpiredLocked(candidate)
			}
		} else {
			for token, candidate := range s.tokens {
				if candidate == secret && s.tokenUnexpiredLocked(candidate) {
					entry = installerInvitation{Token: token, DeviceToken: true}
					exists = true
					break
				}
			}
			if !exists {
				invitation, ok := s.invitations[secret]
				exists = ok && time.Now().Before(invitation.expires)
				entry = installerInvitation{Token: invitation.token, Secret: secret}
			}
		}
		if status, err := s.authorize(in.Identity.request(), exists); err != nil {
			s.mu.Unlock()
			if status == http.StatusUnauthorized {
				err = i18n.NewError("invitation.invalid.expired.or.already.used")
			}
			return installerInvitation{}, &controlError{Status: status, Message: err.Error()}
		}
		if in.Consume && len(secret) == shortCodeLength {
			delete(s.shortCodes, secret)
		}
		s.mu.Unlock()
		return entry, nil
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setRuntimeIdentityHeaders(w.Header())
		mux.ServeHTTP(w, r)
	})
}
