package client

import (
	"agenterm/internal/protocol"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxServerHistory = 10

var bareShortCode = regexp.MustCompile(`^[A-Z0-9]{6}$`)

// IsShortCode recognizes a six-character code case-insensitively.
func IsShortCode(value string) bool {
	return bareShortCode.MatchString(strings.ToUpper(strings.TrimSpace(value)))
}

// ShortCodeForServer keeps the selected server's original scheme and prefix.
func ShortCodeForServer(server, code string) (string, error) {
	if !IsShortCode(code) {
		return "", TokenValidationError()
	}
	u, err := url.Parse(server)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("invalid server address")
	}
	prefix, err := protocol.NormalizePathPrefix(u.Path)
	if err != nil || prefix != u.Path {
		return "", errors.New("invalid server path")
	}
	u.Path = prefix + "/c/" + strings.ToUpper(strings.TrimSpace(code))
	return u.String(), nil
}

func historyAddress(clientURL string) (string, error) {
	u, err := url.Parse(clientURL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Hostname() == "" {
		return "", errors.New("invalid server address")
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	default:
		return "", errors.New("invalid server scheme")
	}
	prefix, ok := strings.CutSuffix(u.Path, "/client")
	if !ok {
		return "", errors.New("invalid server path")
	}
	u.Path = prefix
	value := u.String()
	if _, err := ShortCodeForServer(value, "000000"); err != nil {
		return "", err
	}
	return value, nil
}

// historyAccess serializes processes so simultaneous successful connections do
// not overwrite each other's history. Files contain server addresses only.
func historyAccess(fn func(string) error) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".agenterm")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "servers.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	deadline := time.Now().Add(time.Second)
	for {
		err = tryConnectionLock(lock)
		if err == nil {
			break
		}
		if !connectionLockBusy(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fn(filepath.Join(dir, "servers.json"))
}

func readServerHistory(path string) ([]string, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var values []string
	if err := json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&values); err != nil {
		return nil, err
	}
	result := make([]string, 0, maxServerHistory)
	seen := make(map[string]bool)
	for _, value := range values {
		if _, err := ShortCodeForServer(value, "000000"); err != nil || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
		if len(result) == maxServerHistory {
			break
		}
	}
	return result, nil
}

// ServerHistory returns the most recently connected server first.
func ServerHistory() ([]string, error) {
	var result []string
	err := historyAccess(func(path string) error { var err error; result, err = readServerHistory(path); return err })
	return result, err
}

// RememberServer records only a successful connection's server, never a token.
func RememberServer(clientURL string) error {
	server, err := historyAddress(clientURL)
	if err != nil {
		return err
	}
	return historyAccess(func(path string) error {
		history, err := readServerHistory(path)
		if err != nil {
			return err
		}
		values := []string{server}
		for _, value := range history {
			if value != server && len(values) < maxServerHistory {
				values = append(values, value)
			}
		}
		data, err := json.Marshal(values)
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(path), ".servers-*.tmp")
		if err != nil {
			return err
		}
		temp := f.Name()
		defer os.Remove(temp)
		if err = f.Chmod(0600); err != nil {
			f.Close()
			return err
		}
		if _, err = fmt.Fprintln(f, string(data)); err != nil {
			f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		return os.Rename(temp, path)
	})
}
