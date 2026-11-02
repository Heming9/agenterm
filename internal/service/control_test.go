package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestControlProtocolVersionAndRequestLimit(t *testing.T) {
	s := &runtimeState{clientURL: "ws://example.com/agenterm/client"}
	handler := s.controlHandler(context.Background())
	for _, version := range []string{"", "2"} {
		r := httptest.NewRequest("POST", "/v1/config", strings.NewReader("{}"))
		r.Header.Set(controlVersionHeader, version)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusUpgradeRequired {
			t.Fatalf("version %q: %d", version, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/v1/config", strings.NewReader(`{"padding":"`+strings.Repeat("a", 3<<20)+`"}`))
	r.Header.Set(controlVersionHeader, controlVersion)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized request accepted: %d", w.Code)
	}
}

func TestExecutionSurvivesAbandonedControlWait(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	secret := h.s.tokens[token]
	c := localControl{h.s}
	id, err := controlCall[string](context.Background(), c, "execute/start", toolRequest[executeArgs]{Secret: secret, Args: executeArgs{Command: shortDelayedCommand()}})
	if err != nil {
		t.Fatal(err)
	}
	in := executionRequest{Secret: secret, ID: id}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := controlCall[json.RawMessage](ctx, c, "execute/wait", in); err == nil {
		t.Fatal("cancelled waiter succeeded")
	}
	if _, err := h.s.getExecution(executionRequest{Secret: "another-owner", ID: id}); err == nil {
		t.Fatal("execution crossed credential boundary")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := controlCall[map[string]any](ctx, c, "execute/wait", in)
	if err != nil || result["exit_code"] != float64(0) || !strings.Contains(result["stdout"].(string), "retained") {
		t.Fatalf("abandoned execution: %v %v", result, err)
	}
	h.s.mu.Lock()
	h.s.cleanupTokensLocked(time.Now().Add(31 * time.Minute))
	remaining := len(h.s.executions)
	h.s.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("finished execution did not expire: %d", remaining)
	}
}

func shortDelayedCommand() string {
	if runtime.GOOS == "windows" {
		return `ping -n 2 127.0.0.1 >nul & echo retained`
	}
	return `sleep 0.1; printf retained`
}
