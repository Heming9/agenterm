package protocol

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
)

type Token struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

func Encode(t Token) (string, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func Decode(s string) (Token, error) {
	var t Token
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return t, fmt.Errorf("invalid token: %w", err)
	}
	if err = json.Unmarshal(b, &t); err != nil {
		return t, fmt.Errorf("invalid token: %w", err)
	}
	u, err := url.Parse(t.URL)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" || t.Secret == "" {
		return t, fmt.Errorf("invalid token: missing WebSocket URL or secret")
	}
	return t, nil
}

type Message struct {
	Type        string `json:"type"`
	ID          string `json:"id,omitempty"`
	Command     string `json:"command,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
	Input       string `json:"input,omitempty"`
	Output      string `json:"output,omitempty"`
	ExitCode    int    `json:"exit_code,omitempty"`
	Error       string `json:"error,omitempty"`
}
