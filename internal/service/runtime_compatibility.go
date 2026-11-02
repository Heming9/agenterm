package service

import (
	"agenterm/internal/version"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const runtimeVersionHeader = "X-Agenterm-Runtime-Version"
const runtimePIDHeader = "X-Agenterm-Runtime-Pid"
const runtimeInfoPath = "/_agenterm/runtime-info"

// This read-only identity contract is independent of the versioned business API.
type runtimeIdentity struct {
	Version    string `json:"version"`
	APIVersion string `json:"api_version"`
	PID        int    `json:"pid"`
}

func currentRuntimeIdentity() runtimeIdentity {
	return runtimeIdentity{Version: version.Current, APIVersion: controlVersion, PID: os.Getpid()}
}

func setRuntimeIdentityHeaders(header http.Header) {
	identity := currentRuntimeIdentity()
	header.Set(controlVersionHeader, identity.APIVersion)
	header.Set(runtimeVersionHeader, identity.Version)
	header.Set(runtimePIDHeader, strconv.Itoa(identity.PID))
}

func identityFromHeaders(header http.Header) runtimeIdentity {
	pid, _ := strconv.Atoi(header.Get(runtimePIDHeader))
	if pid < 1 {
		pid = 0
	}
	return runtimeIdentity{Version: header.Get(runtimeVersionHeader), APIVersion: header.Get(controlVersionHeader), PID: pid}
}

type runtimeCompatibilityError struct {
	ServerVersion string
	ServerAPI     string
	Runtime       runtimeIdentity
	Instance      string
}

func diagnosticVersion(value string) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value))
	runes := []rune(value)
	if len(runes) > 80 {
		value = string(runes[:80])
	}
	if value == "" {
		return serverText("runtime.compatibility.unknown")
	}
	return value
}

func (e *runtimeCompatibilityError) Error() string {
	instance := e.Instance
	if instance == "" {
		instance = defaultInstance
	}
	stop := "/path/to/compatible/agenterm-server stop -instance " + instance
	pid, manual := serverText("runtime.compatibility.unknown"), serverText("runtime.compatibility.manual.unknown")
	if e.Runtime.PID > 0 {
		pid = strconv.Itoa(e.Runtime.PID)
		manual = "kill -TERM " + pid
	}
	return fmt.Sprintf(serverText("runtime.compatibility.error"),
		diagnosticVersion(e.ServerVersion), diagnosticVersion(e.ServerAPI),
		diagnosticVersion(e.Runtime.Version), diagnosticVersion(e.Runtime.APIVersion), pid,
		diagnosticVersion(e.Runtime.APIVersion), stop, manual)
}

func withRuntimeInstance(err error, instance string) error {
	var incompatible *runtimeCompatibilityError
	if !errors.As(err, &incompatible) {
		return err
	}
	copy := *incompatible
	copy.Instance = instance
	return &copy
}

func (c *runtimeClient) incompatibleRuntime(ctx context.Context, identity runtimeIdentity) error {
	identity = c.readRuntimeIdentity(ctx, identity)
	return &runtimeCompatibilityError{ServerVersion: version.Current, ServerAPI: controlVersion, Runtime: identity}
}

func (c *runtimeClient) readRuntimeIdentity(ctx context.Context, identity runtimeIdentity) runtimeIdentity {
	if identity.Version != "" && identity.APIVersion != "" && identity.PID > 0 {
		return identity
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://runtime"+runtimeInfoPath, nil)
	if err == nil {
		if response, err := c.http.Do(request); err == nil {
			var reported runtimeIdentity
			if response.StatusCode == http.StatusOK {
				_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&reported)
			}
			response.Body.Close()
			if identity.Version == "" {
				identity.Version = reported.Version
			}
			if identity.APIVersion == "" {
				identity.APIVersion = reported.APIVersion
			}
			if identity.PID == 0 && reported.PID > 0 {
				identity.PID = reported.PID
			}
		}
	}
	// Early API-v1 runtimes predate identity headers and the stable info route.
	// Read only their known config endpoint to recover PID; never retry a
	// mutation under the runtime's protocol version or infer a release version.
	if identity.PID == 0 && identity.APIVersion == "1" {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://runtime/v1/config", bytes.NewBufferString("{}"))
		if err == nil {
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(controlVersionHeader, "1")
			if response, err := c.http.Do(request); err == nil {
				var config runtimeConfig
				if response.StatusCode == http.StatusOK && response.Header.Get(controlVersionHeader) == "1" {
					if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&config) == nil && config.PID > 0 {
						identity.PID = config.PID
					}
				}
				response.Body.Close()
			}
		}
	}
	return identity
}
