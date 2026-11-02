package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

const defaultInstance = "default"
const serverDrainTimeout = 70 * time.Second

var validInstance = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

type instancePaths struct {
	Name, Root, Dir, Runtime, Server, Switch string
}

func pathsForInstance(name string) (instancePaths, error) {
	if !validInstance.MatchString(name) {
		return instancePaths{}, errors.New("instance must be 1–40 lowercase letters, digits, hyphens or underscores, starting with a letter or digit")
	}
	root := filepath.Join(os.TempDir(), "agenterm-"+strconv.Itoa(os.Getuid()))
	dir := root
	if name != defaultInstance {
		dir = filepath.Join(root, name)
	}
	// macOS Unix socket names are limited to 104 bytes. A long TMPDIR must
	// not prevent ordinary instance names from working.
	if len(dir)+30 > 103 {
		root = filepath.Join("/tmp", "agenterm-"+strconv.Itoa(os.Getuid()))
		dir = root
		if name != defaultInstance {
			dir = filepath.Join(root, name)
		}
	}
	return instancePaths{Name: name, Root: root, Dir: dir, Runtime: filepath.Join(dir, "runtime.sock"), Server: filepath.Join(dir, "server.sock"), Switch: filepath.Join(dir, "switch.sock")}, nil
}

func (p instancePaths) prepare() error {
	if err := ensureSocketDirectory(filepath.Join(p.Root, ".owner")); err != nil {
		return err
	}
	return ensureSocketDirectory(p.Runtime)
}

func (p instancePaths) backendSocket() string {
	return filepath.Join(p.Dir, fmt.Sprintf("api-%d-%s.sock", os.Getpid(), randomBytes(4)))
}

// A short-lived Unix listener serializes switches and full shutdowns. It has
// no data file, and listenUnix can reclaim it after an interrupted manager.
func (p instancePaths) lock(ctx context.Context) (net.Listener, error) {
	if err := p.prepare(); err != nil {
		return nil, err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		listener, err := listenUnix(p.Switch)
		if err == nil {
			return listener, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, err
		}
		if info, statErr := os.Lstat(p.Switch); statErr == nil && info.Mode()&os.ModeSocket == 0 {
			return nil, err
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (p instancePaths) currentBackend() (string, bool, error) {
	info, err := os.Lstat(p.Server)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSocket != 0 {
		return p.Server, true, nil
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", false, errors.New("server endpoint is neither a socket nor a managed link")
	}
	target, err := os.Readlink(p.Server)
	if err != nil {
		return "", false, err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(p.Dir, target)
	}
	target = filepath.Clean(target)
	if filepath.Dir(target) != p.Dir || target == p.Server || target == p.Runtime || target == p.Switch {
		return "", false, errors.New("server endpoint points outside its managed backend sockets")
	}
	return target, false, nil
}

func stopBackend(ctx context.Context, socket string) error {
	if socket == "" {
		return nil
	}
	client := &http.Client{Transport: unixTransport(socket)}
	defer client.CloseIdleConnections()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://server/_agenterm/shutdown", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(r)
	if runtimeAbsent(err) {
		return nil
	}
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("server shutdown returned HTTP %d", response.StatusCode)
	}
	return nil
}

func waitBackend(ctx context.Context, socket string) error {
	client := &http.Client{Transport: unixTransport(socket)}
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://server/healthz", nil)
		if err != nil {
			return err
		}
		response, err := client.Do(r)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Publish only a ready backend, then ask its predecessor to drain. The runtime
// continues using the same stable path and never has to restart for a switch.
func publishBackend(ctx context.Context, p instancePaths, candidate string, runtimePID int, control control) error {
	lock, err := p.lock(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	config, err := controlCall[runtimeConfig](ctx, control, "config", struct{}{})
	if err != nil {
		return err
	}
	if config.PID != runtimePID {
		return errors.New("runtime changed during startup; run the server command again")
	}
	previous, legacy, err := p.currentBackend()
	if err != nil {
		return err
	}
	if legacy {
		// A pre-simplification server listened directly on the stable path.
		// Let it unlink that socket before replacing the path with a symlink.
		if err := stopBackend(ctx, previous); err != nil {
			return err
		}
		for {
			conn, dialErr := net.DialTimeout("unix", p.Server, 100*time.Millisecond)
			if dialErr == nil {
				conn.Close()
			} else if runtimeAbsent(dialErr) {
				if err := os.Remove(p.Server); err != nil && !os.IsNotExist(err) {
					return err
				}
				break
			}
			select {
			case <-time.After(10 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	temporary := filepath.Join(p.Dir, ".s-"+randomBytes(4))
	if err := os.Symlink(filepath.Base(candidate), temporary); err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, p.Server); err != nil {
		return err
	}
	if previous != "" && !legacy {
		// The replacement is now serving. Failure to reach a dead predecessor
		// must not bring down the healthy replacement.
		drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := stopBackend(drain, previous); err != nil {
			return &backendDrainError{err}
		}
		if conn, err := net.DialTimeout("unix", previous, 100*time.Millisecond); err == nil {
			conn.Close()
		} else if runtimeAbsent(err) {
			if info, err := os.Lstat(previous); err == nil && info.Mode()&os.ModeSocket != 0 {
				_ = os.Remove(previous)
			}
		}
	}
	return nil
}

type backendDrainError struct{ error }
