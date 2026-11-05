package client

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testHistoryHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestServerHistory(t *testing.T) {
	home := testHistoryHome(t)
	for _, server := range []string{"ws://one.example:8080/custom/client", "wss://two.example/agenterm/client", "ws://one.example:8080/custom/client"} {
		if err := RememberServer(server); err != nil {
			t.Fatal(err)
		}
	}
	servers, err := ServerHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers[0] != "http://one.example:8080/custom" || servers[1] != "https://two.example/agenterm" {
		t.Fatalf("history: %v", servers)
	}
	value, err := ShortCodeForServer(servers[0], "a1b2c3")
	if err != nil || value != "http://one.example:8080/custom/c/A1B2C3" {
		t.Fatalf("link: %s %v", value, err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".agenterm", "servers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "A1B2C3") || strings.Contains(string(data), "/client") {
		t.Fatal("history contains a credential or client route")
	}

}

func TestServerHistoryRejectsUnsafeAddresses(t *testing.T) {
	testHistoryHome(t)
	for _, value := range []string{"https://user:pass@host/prefix", "https://host/prefix?x=1", "https://host/a/../b", "https://host/#fragment", "ftp://host"} {
		if _, err := ShortCodeForServer(value, "ABC123"); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	for _, value := range []string{"ABC12", "ABC1234", "http://host/c/ABC123"} {
		if IsShortCode(value) {
			t.Errorf("accepted code %q", value)
		}
	}
}

func TestServerHistoryConcurrentWrites(t *testing.T) {
	testHistoryHome(t)
	addresses := []string{"wss://one.example/agenterm/client", "ws://two.example/client", "wss://three.example/custom/client"}
	var wg sync.WaitGroup
	errs := make(chan error, len(addresses))
	for _, address := range addresses {
		wg.Add(1)
		go func(address string) { defer wg.Done(); errs <- RememberServer(address) }(address)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	history, err := ServerHistory()
	if err != nil || len(history) != len(addresses) {
		t.Fatalf("history=%v err=%v", history, err)
	}
}
