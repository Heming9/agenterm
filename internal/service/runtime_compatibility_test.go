package service

import (
	"agenterm/internal/version"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

type compatibilityTransport struct{ handler http.Handler }

func (transport compatibilityTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	transport.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func compatibilityClient(handler http.HandlerFunc) *runtimeClient {
	return &runtimeClient{http: &http.Client{Transport: compatibilityTransport{handler}}}
}

func TestRuntimeIdentityIndependentOfProtocol(t *testing.T) {
	handler := (&runtimeState{}).controlHandler()
	for _, path := range []string{"/v1/config", "/v99/config", runtimeInfoPath} {
		method := http.MethodPost
		if path == runtimeInfoPath {
			method = http.MethodGet
		}
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		r.Header.Set(controlVersionHeader, "incompatible")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if identity := identityFromHeaders(w.Header()); identity != currentRuntimeIdentity() {
			t.Fatalf("%s: identity %+v", path, identity)
		}
		if path == runtimeInfoPath {
			var identity runtimeIdentity
			if err := json.Unmarshal(w.Body.Bytes(), &identity); w.Code != http.StatusOK || err != nil || identity != currentRuntimeIdentity() {
				t.Fatalf("version-independent identity: %d %s", w.Code, w.Body)
			}
		}
	}
}

func TestRuntimeCompatibilityDiagnostics(t *testing.T) {
	for _, status := range []int{http.StatusUpgradeRequired, http.StatusOK, http.StatusNotFound} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			requests := 0
			c := compatibilityClient(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/v1/config" || r.Header.Get(controlVersionHeader) != controlVersion {
					t.Errorf("unexpected retry: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set(controlVersionHeader, "4")
				w.Header().Set(runtimeVersionHeader, "0.4.0")
				w.Header().Set(runtimePIDHeader, "12345")
				w.WriteHeader(status)
			})
			err := c.call(context.Background(), "config", struct{}{}, &runtimeConfig{})
			var incompatible *runtimeCompatibilityError
			if !errors.As(err, &incompatible) || requests != 1 {
				t.Fatalf("expected one rejected request: %v, requests=%d", err, requests)
			}
			for _, language := range []string{"en", "zh-CN"} {
				t.Setenv("AGENTERM_LANG", language)
				message := withRuntimeInstance(err, "staging").Error()
				for _, part := range []string{version.Current, "API " + controlVersion, "0.4.0", "API 4", "runtime PID 12345", "stop -instance staging", "kill -TERM 12345"} {
					if !strings.Contains(message, part) {
						t.Errorf("%s diagnostic missing %q: %s", language, part, message)
					}
				}
				if strings.Contains(message, "%!") {
					t.Fatal("invalid localized format: " + message)
				}
			}
			if incompatible.Instance != "" {
				t.Fatal("instance annotation mutated the original error")
			}
		})
	}
}

func TestRuntimeCompatibilityReadOnlyFallback(t *testing.T) {
	requests := 0
	c := compatibilityClient(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == runtimeInfoPath && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(runtimeIdentity{Version: "0.4.0", APIVersion: "2", PID: os.Getpid()})
			return
		}
		w.WriteHeader(http.StatusUpgradeRequired)
	})
	err := c.call(context.Background(), "config", struct{}{}, &runtimeConfig{})
	var incompatible *runtimeCompatibilityError
	if !errors.As(err, &incompatible) || incompatible.Runtime != (runtimeIdentity{Version: "0.4.0", APIVersion: "2", PID: os.Getpid()}) || requests != 2 {
		t.Fatalf("read-only diagnostic fallback failed: %+v, %v, requests=%d", incompatible, err, requests)
	}

	// Legacy API-v1 runtimes expose PID through config, but no release version.
	c = compatibilityClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == runtimeInfoPath && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path != "/v1/config" || r.Method != http.MethodPost || r.Header.Get(controlVersionHeader) != "1" {
			t.Fatalf("diagnostic must only read legacy config: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set(controlVersionHeader, "1")
		_ = json.NewEncoder(w).Encode(runtimeConfig{PID: os.Getpid()})
	})
	identity := c.readRuntimeIdentity(context.Background(), runtimeIdentity{APIVersion: "1"})
	if identity.PID != os.Getpid() || identity.Version != "" {
		t.Fatalf("legacy PID or unknown version: %+v", identity)
	}
	message := (&runtimeCompatibilityError{Runtime: identity}).Error()
	if !strings.Contains(message, serverText("runtime.compatibility.unknown")) {
		t.Fatal("legacy release version should be unknown: " + message)
	}
}

func TestRuntimeCompatibilityMissingIdentity(t *testing.T) {
	requests := 0
	c := compatibilityClient(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/config" && (r.Method != http.MethodGet || r.URL.Path != runtimeInfoPath) {
			t.Fatalf("unexpected diagnostic request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set(runtimePIDHeader, "-1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	})
	err := c.call(context.Background(), "config", struct{}{}, &runtimeConfig{})
	var incompatible *runtimeCompatibilityError
	if !errors.As(err, &incompatible) || requests != 2 || incompatible.Runtime.PID != 0 {
		t.Fatalf("missing identity: %v, requests=%d", err, requests)
	}
	message := err.Error()
	if strings.Contains(message, "kill -TERM 0") || !strings.Contains(message, "kill -TERM <PID>") {
		t.Fatal("missing PID must never produce a usable signal command: " + message)
	}
}

func TestCompatibleRuntimeErrorsPreserved(t *testing.T) {
	c := compatibilityClient(func(w http.ResponseWriter, r *http.Request) {
		setRuntimeIdentityHeaders(w.Header())
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(controlError{Status: http.StatusUnauthorized, Message: "unauthorized"})
	})
	err := c.call(context.Background(), "auth", struct{}{}, &runtimeConfig{})
	var ce *controlError
	if !errors.As(err, &ce) || ce.Status != http.StatusUnauthorized || ce.Message != "unauthorized" {
		t.Fatalf("compatible runtime error changed: %v", err)
	}
}
