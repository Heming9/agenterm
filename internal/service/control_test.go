package service

import (
	terminal "agenterm/internal/client"

	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestControlProtocolVersionAndRequestLimit(t *testing.T) {
	s := &runtimeState{clientURL: "ws://example.com/agenterm/client"}
	handler := s.controlHandler()
	for _, version := range []string{"", "1", "2"} {
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

func TestExecutionThroughControlAPI(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	out, err := controlCall[map[string]any](context.Background(), localControl{h.s}, "terminal/execute", toolRequest[executeArgs]{Secret: h.s.tokens[token], Args: executeArgs{ExecuteRequest: terminal.ExecuteRequest{Command: shortDelayedCommand()}}})
	if err != nil || out["exit_code"] != float64(0) || !strings.Contains(out["output"].(string), "retained") {
		t.Fatal(out, err)
	}
}

func shortDelayedCommand() string {
	if runtime.GOOS == "windows" {
		return `Start-Sleep -Milliseconds 100; Write-Output retained`
	}
	return `sleep 0.1; printf retained`
}
