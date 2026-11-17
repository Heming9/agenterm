//go:build !windows

package service

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerCLIOptionsAndSeparateCommands(t *testing.T) {
	var help bytes.Buffer
	_, err := parseServerCommand([]string{"-help"}, &help)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
	wanted := []string{"client-ip-header", "enable-short-code", "instance", "listen", "max-clients-per-ip", "no-password", "passwd", "passwd-file", "public-url", "stdio", "url-prefix"}
	actual := make([]string, 0)
	for _, line := range strings.Split(help.String(), "\n") {
		if strings.HasPrefix(line, "  -") {
			actual = append(actual, strings.Fields(line)[0][1:])
		}
	}
	if strings.Join(actual, ",") != strings.Join(wanted, ",") {
		t.Fatalf("help options: %v", actual)
	}
	for _, name := range []string{"start", "stop", "restart", "status"} {
		command, err := parseServerCommand([]string{name, "-instance", "staging"}, &bytes.Buffer{})
		if err != nil || command.Action != name || command.Paths.Name != "staging" {
			t.Fatalf("%s: %+v %v", name, command, err)
		}
	}
	command, err := parseServerCommand(nil, &bytes.Buffer{})
	if err != nil || command.Action != "start" || command.Paths.Name != "default" || command.Options.Settings.Prefix != "/agenterm" {
		t.Fatalf("defaults: %+v %v", command, err)
	}
}

func TestCLIRejectsRemovedFlagsAndInvalidCommandsBeforeStarting(t *testing.T) {
	for _, name := range []string{"runtime-socket", "listen-socket", "server-socket", "generate", "shutdown-timeout", "stop", "restart-runtime", "check"} {
		if _, err := parseServerCommand([]string{"-" + name}, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted removed flag: %s", name)
		}
	}
	for _, args := range [][]string{{"runtime"}, {"start", "extra"}, {"stop", "-passwd-file", "key"}, {"status", "-stdio"}, {"restart", "-passwd-file", ""}, {"restart", "-public-url", "bad"}, {"-instance", "../escape"}, {"-instance", "WithSpaces "}} {
		if _, err := parseServerCommand(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted: %v", args)
		}
	}
}

func managementKeyFile(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "management-key")
	if err := os.WriteFile(path, []byte(key), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestManagementKeyFile(t *testing.T) {
	for _, data := range []string{"key", "key\n", "key\r\n"} {
		key, err := readManagementKey(managementKeyFile(t, data))
		if err != nil || key != "key" {
			t.Fatal("cannot read single-line key", err)
		}
	}
	for _, value := range []string{"", "one\ntwo", "one\x00two", "one\x7ftwo", "one\xfftwo", "key\n\r\n", strings.Repeat("a", 65537)} {
		if _, err := parseServerCommand([]string{"restart", "-passwd-file", managementKeyFile(t, value)}, &bytes.Buffer{}); err == nil {
			t.Fatal("accepted invalid key file")
		}
	}
	if _, err := parseServerCommand([]string{"restart", "-passwd-file", managementKeyFile(t, "key"), "-no-password"}, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted conflicting authentication settings")
	}
	if _, err := parseServerCommand([]string{"-passwd", "inline-key", "-passwd-file", managementKeyFile(t, "file-key")}, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted two management key sources")
	}
}

func TestStatusDoesNotCreateOrStartAnInstance(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ag-status-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("TMPDIR", dir)
	// Read-only status must not create even an explicitly configured log file,
	// or be blocked by an invalid logging configuration.
	t.Setenv("AGENTERM_LOG_FILE", filepath.Join(dir, "must-not-exist.log"))
	t.Setenv("AGENTERM_LOG_FORMAT", "invalid")
	err = runServerCommand([]string{"status", "-instance", "unused"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("status created files: %v %v", entries, err)
	}
}

func TestInstanceNamesIsolateManagedSockets(t *testing.T) {
	a, err := pathsForInstance("default")
	if err != nil {
		t.Fatal(err)
	}
	b, err := pathsForInstance("staging")
	if err != nil {
		t.Fatal(err)
	}
	if a.Runtime == b.Runtime || a.Server == b.Server || a.Switch == b.Switch {
		t.Fatal("instances share endpoints")
	}
	if filepath.Dir(b.Runtime) != b.Dir || filepath.Dir(b.Server) != b.Dir {
		t.Fatal("endpoint escaped its instance")
	}
	if _, err := pathsForInstance(strings.Repeat("a", 41)); err == nil {
		t.Fatal("accepted oversized instance name")
	}
}
