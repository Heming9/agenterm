package client

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// All client versions and frontends share this directory. Keep it outside the
// download cache, which can be deleted while a connection is still running.
func acquireConnectionLock(serverURL string) (*os.File, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, i18n.NewError("client.connection.lock.failed", err)
	}
	directory := filepath.Join(home, ".agenterm", "locks")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, i18n.NewError("client.connection.lock.failed", err)
	}
	key, err := connectionLockKey(serverURL)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(directory, key+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, i18n.NewError("client.connection.lock.failed", err)
	}
	if err := tryConnectionLock(file); err != nil {
		file.Close()
		if connectionLockBusy(err) {
			return nil, i18n.NewError("client.already.connected.to.server")
		}
		return nil, i18n.NewError("client.connection.lock.failed", err)
	}
	// Closing the handle releases the OS lock, including on process termination.
	// Never unlink the file: another process could still hold its inode open.
	return file, nil
}

// connectionLockKey identifies the connection endpoint independently of URL spelling.
func connectionLockKey(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "wss" {
			port = "443"
		}
	}
	address := u.Scheme + "://" + net.JoinHostPort(strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), port)
	// Preserve existing lock names for the default route. Custom prefixes can
	// identify independent servers hosted behind the same origin.
	if u.Path != protocol.ClientPath {
		address += u.Path
	}
	key := sha256.Sum256([]byte(address))
	return hex.EncodeToString(key[:]), nil
}
