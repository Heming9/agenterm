package service

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPublicIngressCannotStopServer(t *testing.T) {
	var stopped atomic.Bool
	front := &frontend{shutdown: func() { stopped.Store(true) }}
	handler := front.socketHandler(http.NotFoundHandler())
	r := httptest.NewRequest(http.MethodPost, "/_agenterm/shutdown", nil)
	r.Header.Set(remoteAddrHeader, "127.0.0.1:12345")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || stopped.Load() {
		t.Fatal("public ingress stopped server")
	}
	r.Header.Del(remoteAddrHeader)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted || !stopped.Load() {
		t.Fatal("private management did not stop server")
	}
}

func TestInvalidRuntimeOptionsAreRejectedBeforeRestart(t *testing.T) {
	o := &runtimeLaunchOptions{Settings: runtimeSettings{Listen: "127.0.0.1:8080", PublicURL: "http://localhost:8080", Prefix: "/agenterm", ServerSocket: "/tmp/test.sock"}, Fields: map[string]bool{"passwd": true}}
	if err := validateRuntimeLaunch(o); err == nil {
		t.Fatal("empty explicit password accepted")
	}
	o.Fields = map[string]bool{}
	o.Settings.MaxClientsPerIP = -1
	if err := validateRuntimeLaunch(o); err == nil {
		t.Fatal("invalid quota accepted")
	}
}
