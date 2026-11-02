package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

const testMCPVersion = "2026-07-28"

func setMCPHeaders(r *http.Request) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", testMCPVersion)
	if r.Body == nil {
		return
	}
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	var request map[string]any
	if json.Unmarshal(body, &request) == nil {
		params, _ := request["params"].(map[string]any)
		if params == nil {
			params = make(map[string]any)
			request["params"] = params
		}
		if method, ok := request["method"].(string); ok {
			r.Header.Set("Mcp-Method", method)
		}
		if name, ok := params["name"].(string); ok {
			r.Header.Set("Mcp-Name", name)
		}
		if request["method"] == "initialize" {
			if version, ok := params["protocolVersion"].(string); ok {
				r.Header.Set("MCP-Protocol-Version", version)
			}
		} else {
			params["_meta"] = map[string]any{
				"io.modelcontextprotocol/protocolVersion":    testMCPVersion,
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "agenterm-tests", "version": "0.3.0"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			}
		}
		body, _ = json.Marshal(request)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
}

func decodeToolText(text string, isError bool) (any, error) {
	var value any
	err := json.Unmarshal([]byte(text), &value)
	if err != nil && isError {
		return map[string]any{"error": text}, nil
	}
	return value, err
}
