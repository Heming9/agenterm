package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"agenterm/internal/client"
)

func isCLILinkLaunch(args []string) bool {
	return (len(args) == 1 && strings.HasPrefix(args[0], "agenterm-cli:")) || (len(args) == 2 && args[0] == "-link-file")
}

// Keep the registered handler outside installer caches and temporary directories.
// A content-addressed name also avoids replacing an executable in use on Windows.
func stableCLILinkExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return copyCLILinkExecutable(executable, filepath.Join(dir, "Agenterm", "cli"))
}

func copyCLILinkExecutable(source, dir string) (string, error) {
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	h := sha256.New()
	if _, err = io.Copy(h, input); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	name := "agenterm-cli-" + digest
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, name)
	if info, e := os.Lstat(dest); e == nil && !info.Mode().IsRegular() {
		return "", fmt.Errorf("existing Agenterm link handler is not a regular file")
	} else if e != nil && !os.IsNotExist(e) {
		return "", e
	}
	if f, e := os.Open(dest); e == nil {
		defer f.Close()
		check := sha256.New()
		if _, e = io.Copy(check, f); e == nil && hex.EncodeToString(check.Sum(nil)) == digest {
			return dest, nil
		}
		return "", fmt.Errorf("existing Agenterm link handler has an unexpected checksum")
	} else if !os.IsNotExist(e) {
		return "", e
	}
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".agenterm-cli-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	check := sha256.New()
	if _, err = io.Copy(io.MultiWriter(tmp, check), input); err != nil {
		tmp.Close()
		return "", err
	}
	if hex.EncodeToString(check.Sum(nil)) != digest {
		tmp.Close()
		return "", fmt.Errorf("Agenterm executable changed during registration")
	}
	if err = tmp.Chmod(0700); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(tmp.Name(), dest); err != nil {
		// Another simultaneous launch may have installed the same binary.
		if f, e := os.Open(dest); e == nil {
			defer f.Close()
			check := sha256.New()
			if _, e = io.Copy(check, f); e == nil && hex.EncodeToString(check.Sum(nil)) == digest {
				return dest, nil
			}
		}
		return "", err
	}
	return dest, nil
}

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
	raw := string(b)
	if _, _, err := client.ParseDeepLink(raw, "agenterm-cli"); err != nil {
		return "", err
	}
	return raw, nil
}

func deepLinkArgs(args []string) ([]string, error) {
	raw := ""
	if len(args) == 1 && strings.HasPrefix(args[0], "agenterm-cli:") {
		raw = args[0]
	}
	if len(args) == 2 && args[0] == "-link-file" {
		var err error
		raw, err = linkFile(args[1])
		if err != nil {
			return nil, err
		}
	}
	if raw == "" {
		return args, nil
	}
	address, mode, err := client.ParseDeepLink(raw, "agenterm-cli")
	if err != nil {
		return nil, err
	}
	result := []string{"-token", address}
	if mode != "" {
		result = append(result, "-connection-mode", mode)
	}
	return result, nil
}

func linkInbox() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Agenterm", "link-inbox"), nil
}
