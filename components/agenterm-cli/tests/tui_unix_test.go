//go:build darwin || linux

package interop

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// Capture and replay the real terminal stream, including cursor-positioned
// incremental updates. Searching raw stdout cannot validate a TUI screen.
type tuiCapture struct {
	sync.Mutex
	data          string
	width, height int
}

func (c *tuiCapture) Write(p []byte) (int, error) {
	c.Lock()
	c.data += string(p)
	c.Unlock()
	return len(p), nil
}
func (c *tuiCapture) screen() string {
	c.Lock()
	data := c.data
	c.Unlock()
	grid := make([][]string, c.height)
	for y := range grid {
		grid[y] = make([]string, c.width)
		for x := range grid[y] {
			grid[y][x] = " "
		}
	}
	x, y := 0, 0
	var state byte
	for len(data) > 0 {
		seq, cells, n, next := ansi.DecodeSequence(data, state, nil)
		if n == 0 {
			break
		}
		data = data[n:]
		state = next
		if strings.HasPrefix(seq, "\x1b[") {
			if len(seq) < 3 {
				continue
			}
			op := seq[len(seq)-1]
			args := seq[2 : len(seq)-1]
			parts := strings.Split(args, ";")
			number := func(i, def int) int {
				if i >= len(parts) {
					return def
				}
				n, _ := strconv.Atoi(parts[i])
				if n == 0 {
					return def
				}
				return n
			}
			switch op {
			case 'H', 'f':
				y = number(0, 1) - 1
				x = number(1, 1) - 1
			case 'G':
				x = number(0, 1) - 1
			case 'd':
				y = number(0, 1) - 1
			case 'A':
				y -= number(0, 1)
			case 'B':
				y += number(0, 1)
			case 'C':
				x += number(0, 1)
			case 'D':
				x -= number(0, 1)
			case 'J':
				if args == "2" || args == "3" {
					for row := range grid {
						for col := range grid[row] {
							grid[row][col] = " "
						}
					}
				}
			case 'K':
				if y >= 0 && y < c.height {
					for col := max(0, x); col < c.width; col++ {
						grid[y][col] = " "
					}
				}
			case 'h':
				if args == "?1049" {
					x, y = 0, 0
					for row := range grid {
						for col := range grid[row] {
							grid[row][col] = " "
						}
					}
				}
			}
			continue
		}
		if seq == "\r" {
			x = 0
			continue
		}
		if seq == "\n" {
			y++
			continue
		}
		if cells <= 0 {
			continue
		}
		if x >= c.width {
			x = 0
			y++
		}
		if y >= 0 && y < c.height && x >= 0 && x+cells <= c.width {
			grid[y][x] = seq
			for col := x + 1; col < x+cells; col++ {
				grid[y][col] = ""
			}
		}
		x += cells
	}
	rows := make([]string, len(grid))
	for y := range grid {
		rows[y] = strings.TrimRight(strings.Join(grid[y], ""), " ")
	}
	return strings.Join(rows, "\n")
}
func (c *tuiCapture) wait(t *testing.T, text string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		screen := c.screen()
		if strings.Contains(screen, text) {
			return screen
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("screen never showed %q:\n%s", text, c.screen())
	return ""
}

type liveTUI struct {
	capture     *tuiCapture
	terminal    *os.File
	peers       chan *websocket.Conn
	peer        *websocket.Conn
	command     *exec.Cmd
	done        chan error
	redemptions atomic.Int32
	token       string
}

func newLiveTUI(t *testing.T, invite bool, linkMode string, controllingTerminal bool) *liveTUI {
	t.Helper()
	binary := os.Getenv("AGENTERM_RUST_CLI")
	if binary == "" {
		t.Skip("set AGENTERM_RUST_CLI to run real PTY tests")
	}
	f := &liveTUI{capture: &tuiCapture{width: 80, height: 16}, peers: make(chan *websocket.Conn, 8), done: make(chan error, 1)}
	secret := base64.RawURLEncoding.EncodeToString([]byte("device-key"))
	tokenReady := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/agenterm/invite/redeem", func(w http.ResponseWriter, r *http.Request) {
		<-tokenReady
		f.redemptions.Add(1)
		w.Header().Set(version.Header, version.Protocol)
		json.NewEncoder(w).Encode(map[string]string{"token": f.token})
	})
	mux.HandleFunc("/agenterm/c/ABC123", func(w http.ResponseWriter, r *http.Request) {
		<-tokenReady
		f.redemptions.Add(1)
		w.Header().Set(version.Header, version.Protocol)
		json.NewEncoder(w).Encode(map[string]string{"token": f.token})
	})
	mux.HandleFunc("/agenterm/client/abc123", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "unauthorized", 401)
			return
		}
		headers := http.Header{version.Header: {version.Protocol}, protocol.ClientAPIHeader: {"1"}, protocol.ResumeCredentialHeader: {"tui-resume"}}
		peer, err := (&websocket.Upgrader{}).Upgrade(w, r, headers)
		if err == nil {
			f.peers <- peer
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	var err error
	f.token, err = protocol.Encode(protocol.Token{URL: "ws" + strings.TrimPrefix(server.URL, "http") + "/agenterm/client", Secret: secret, Identifier: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	close(tokenReady)
	args := []string{"-token", f.token, "-connection-mode", "persistent"}
	if invite {
		args = []string{"-invite", f.token}
	}
	if linkMode != "" {
		query := neturl.Values{"url": {server.URL + "/agenterm/c/ABC123"}}
		if linkMode != "default" {
			query.Set("mode", linkMode)
		}
		args = []string{"agenterm-cli://connect?" + query.Encode()}
	}
	if controllingTerminal {
		// Match the one-click installer: authorization is already complete,
		// and the client reads terminal input from /dev/tty after curl | sh.
		args = []string{"-invite", f.token, "-consent-confirmed", "-connection-mode=temporary"}
		f.command = exec.Command("sh", append([]string{"-c", `exec "$@" </dev/tty`, "sh", binary}, args...)...)
	} else {
		f.command = exec.Command(binary, args...)
	}
	home := t.TempDir()
	f.command.Env = append(os.Environ(), "AGENTERM_SKIP_LINK_REGISTRATION=1", "HOME="+home, "USERPROFILE="+home, "AGENTERM_LANG=en", "NO_COLOR=1", "TERM=xterm-256color", "SHELL=/bin/sh", "SSH_CONNECTION=local-test")
	f.terminal, err = pty.StartWithSize(f.command, &pty.Winsize{Cols: 80, Rows: 16})
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan struct{})
	go func() { io.Copy(f.capture, f.terminal); close(readDone) }()
	go func() { f.done <- f.command.Wait() }()
	t.Cleanup(func() {
		f.command.Process.Signal(os.Interrupt)
		select {
		case <-f.done:
		case <-time.After(6 * time.Second):
			f.command.Process.Kill()
			<-f.done
			t.Error("TUI failed to shut down")
		}
		if f.peer != nil {
			f.peer.Close()
		}
		f.terminal.Close()
		<-readDone
	})
	return f
}
func (f *liveTUI) nextPeer(t *testing.T) {
	t.Helper()
	select {
	case f.peer = <-f.peers:
	case <-time.After(10 * time.Second):
		t.Fatal("TUI did not connect:\n" + f.capture.screen())
	}
}
func (f *liveTUI) write(t *testing.T, data string) {
	t.Helper()
	if _, err := io.WriteString(f.terminal, data); err != nil {
		t.Fatal(err)
	}
}
func (f *liveTUI) execute(t *testing.T, command string) {
	t.Helper()
	request, _ := json.Marshal(map[string]any{"method": "execute", "params": map[string]any{"command": command, "wait_ms": 5000}})
	if err := f.peer.WriteJSON(protocol.Message{Type: "api_call", ID: "tui-command", Request: request}); err != nil {
		t.Fatal(err)
	}
	f.peer.SetReadDeadline(time.Now().Add(8 * time.Second))
	var reply protocol.Message
	if err := f.peer.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != "" {
		t.Fatal(reply.Error)
	}
}

func TestRustTUIConnectionLogsAndMouse(t *testing.T) {
	f := newLiveTUI(t, false, "", false)
	f.nextPeer(t)
	f.capture.wait(t, "Connected (persistent)")
	f.capture.wait(t, "Client connected. Waiting for remote operations...")
	binary, err := os.Open(f.command.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer binary.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, binary); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", digest.Sum(nil))
	f.capture.wait(t, "sha256: "+hash[:8]+"......"+hash[len(hash)-8:])
	username := strconv.Itoa(os.Geteuid())
	if account, err := user.LookupId(username); err == nil {
		username = account.Username
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	identity := ansi.Truncate(username+"@"+host, f.capture.width-4, "…")
	screen := f.capture.wait(t, identity)
	if divider := strings.Split(screen, "\n")[f.capture.height-3]; !strings.HasSuffix(divider, " "+identity) {
		t.Fatalf("identity missing from footer divider: %q", divider)
	}
	if err := f.peer.WriteJSON(protocol.Message{Type: "latency", LatencyMS: 42}); err != nil {
		t.Fatal(err)
	}
	f.capture.wait(t, "0 commands · 42ms")
	f.execute(t, "printf 'first line\\nsecond line\\nthird line\\n'")
	f.capture.wait(t, "1 commands · 42ms")
	screen = f.capture.wait(t, "[3 lines]")
	if strings.Contains(screen, "Client connected. Waiting for remote operations...") {
		t.Fatal("waiting message still visible after the first command")
	}
	if strings.Contains(screen, "\nsecond line\n") {
		t.Fatal("multiline log should initially be folded:\n" + screen)
	}
	// Row 3 contains the folded command; row 4 contains its output.
	f.write(t, "\x1b[<0;1;4M\x1b[<0;1;4m")
	f.capture.wait(t, "\nsecond line\n")
	// Drag a selection across two lines. SSH forces OSC 52, preserving the user's clipboard.
	f.write(t, "\x1b[<0;3;4M\x1b[<32;5;5M\x1b[<0;5;5m")
	f.capture.wait(t, "Clipboard request sent")
	f.capture.Lock()
	copied := strings.Contains(f.capture.data, "\x1b]52;c;")
	f.capture.Unlock()
	if !copied {
		t.Fatal("selection did not emit OSC 52")
	}
	f.write(t, "\x1b")
	f.capture.wait(t, "Ctrl+C disconnect")
	// A transport failure preserves the UI and reconnects to the same server.
	f.peer.Close()
	f.capture.wait(t, "Reconnecting")
	f.nextPeer(t)
	f.capture.wait(t, "Connection restored.")
	f.write(t, "\x03")
	f.capture.wait(t, "Token or code:")
	f.write(t, "\x03")
	f.capture.wait(t, "Paste connection token")
}

func TestRustTUIInvitationAndReusableCredential(t *testing.T) {
	f := newLiveTUI(t, true, "", false)
	// Consent is rendered in the normal terminal before the alternate screen.
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.capture.Lock()
		ready := strings.Contains(f.capture.data, "Allow authorization (temporary)")
		f.capture.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing authorization menu")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-f.peers:
		t.Fatal("connected before authorization")
	default:
	}
	f.write(t, "\x1b[C\r")
	f.nextPeer(t)
	f.capture.wait(t, "Connected (persistent)")
	f.capture.wait(t, "[4/4] Connected")
	if f.redemptions.Load() != 1 {
		t.Fatal("invitation redemption missing")
	}
	f.peer.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	f.peer.Close()
	f.capture.wait(t, "Token or code:")
	f.write(t, "\r")
	f.capture.wait(t, "Choose connection mode:")
	f.write(t, "\r")
	f.nextPeer(t)
	f.capture.wait(t, "Connected (temporary)")
	if f.redemptions.Load() != 1 {
		t.Fatal(fmt.Sprintf("consumed invitation was redeemed %d times", f.redemptions.Load()))
	}
}

func TestRustPlainPromptInterrupt(t *testing.T) {
	binary := os.Getenv("AGENTERM_RUST_CLI")
	if binary == "" {
		t.Skip("set AGENTERM_RUST_CLI to run real PTY tests")
	}
	command := exec.Command(binary, "-plain")
	command.Env = append(os.Environ(), "AGENTERM_SKIP_LINK_REGISTRATION=1", "AGENTERM_LANG=en", "NO_COLOR=1")
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	capture := &tuiCapture{width: 80, height: 16}
	go io.Copy(capture, terminal)
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	finished := false
	t.Cleanup(func() {
		if !finished {
			command.Process.Kill()
			<-done
		}
	})
	capture.wait(t, "Connection token, short code")
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		finished = true
	case <-time.After(2 * time.Second):
		t.Fatal("plain input prompt swallowed SIGINT")
	}
}

func TestRustTUIControllingTerminalStdin(t *testing.T) {
	f := newLiveTUI(t, true, "", true)
	f.nextPeer(t)
	f.capture.wait(t, "Connected (temporary)")
	if f.redemptions.Load() != 1 {
		t.Fatal("invitation was not redeemed exactly once")
	}
	f.write(t, "\x03")
	f.capture.wait(t, "Token or code:")
}

func TestRustTUIProtocolLinkModes(t *testing.T) {
	for _, mode := range []string{"default", "temporary", "persistent"} {
		t.Run(mode, func(t *testing.T) {
			f := newLiveTUI(t, false, mode, false)
			f.nextPeer(t)
			expected := mode
			if mode == "default" {
				expected = "temporary"
			}
			f.capture.wait(t, "Connected ("+expected+")")
			if f.redemptions.Load() != 1 {
				t.Fatal("short code was not redeemed exactly once")
			}
			f.execute(t, "printf 'linked terminal works\\n'")
			f.capture.wait(t, "linked terminal works")
		})
	}
}
