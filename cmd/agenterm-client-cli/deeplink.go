package main

import (
	"agenterm/internal/client"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// linkFile is produced by the macOS URL handler so the credential is not put in shell history.
func linkFile(path string) (string, error) {
	dir, err := linkInbox()
	if err != nil {
		return "", err
	}
	return consumeLinkFile(path, dir)
}

func consumeLinkFile(path, dir string) (string, error) {
	if filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), "agenterm-link.") {
		return "", fmt.Errorf("invalid link file")
	}
	entry, err := os.Lstat(path)
	if err != nil || !entry.Mode().IsRegular() {
		return "", fmt.Errorf("invalid link file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("invalid link file")
	}
	b, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || len(b) > 8192 {
		return "", fmt.Errorf("invalid link file")
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return client.ParseDeepLink(string(b), "agenterm-cli")
}

func deepLinkArgs(args []string) ([]string, error) {
	if len(args) == 1 && strings.HasPrefix(args[0], "agenterm-cli:") {
		address, err := client.ParseDeepLink(args[0], "agenterm-cli")
		if err != nil {
			return nil, err
		}
		return []string{"-token", address}, nil
	}
	if len(args) == 2 && args[0] == "-link-file" {
		address, err := linkFile(args[1])
		if err != nil {
			return nil, err
		}
		return []string{"-token", address}, nil
	}
	return args, nil
}

func linkInbox() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Agenterm", "link-inbox"), nil
}
