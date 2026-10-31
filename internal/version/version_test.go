package version

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGUIVersionMatchesConnectionVersion(t *testing.T) {
	if err := Check(Current, Current); err != nil {
		t.Fatalf("invalid application version: %v", err)
	}
	metadata, err := os.ReadFile(filepath.Join("..", "..", "cmd", "agenterm-gui", "FyneApp.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metadata), `Version = "`+Current+`"`) {
		t.Fatalf("GUI metadata must use connection version %s", Current)
	}
}

func TestCompatibility(t *testing.T) {
	for _, tc := range []struct {
		local, peer string
		compatible  bool
	}{
		{"0.2.13", "0.2.12", true},
		{"v0.2.13", "0.2.99+build.2", true},
		{"0.0.1", "0.0.1+build", true},
		{"0.0.1", "0.0.2", false},
		{"1.2.13", "1.0.0", true},
		{"1.2.13", "1.9.0+build.2", true},
		{"v1.2.13-rc.1+build.1", "1.2.13-rc.1+build.2", true},
		{"1.2.13-rc.1", "1.2.13-rc.2", false},
		{"1.2.13-rc.1", "1.2.13", false},
		{"1.2.13-rc.1", "1.3.13-rc.1", false},
		{"1.2.13-rc.1", "1.2.14-rc.1", false},
		{"0.2.13-alpha", "0.2.13-alpha+build", true},
		{"0.2.13", "0.2.99-rc.1+build.2", false},
		{"1.2.13", "1.2.13-01", false},
		{"1.2.13", "1.2.13-alpha.01", false},
		{"1.2.13-0", "1.2.13-0", true},
		{"1.2.13-alpha-01", "1.2.13-alpha-01", true},
		{"1.2.13+01", "1.2.13+02", true},
		{"0.2.13", "0.1.13", false},
		{"0.2.13", "0.3.0", false},
		{"0.2.13", "1.2.13", false},
		{"1.2.13", "2.2.13", false},
		{"0.2.13", "", false},
		{"0.2.13", "0.2", false},
		{"0.2.13", "0.02.13", false},
		{"0.2.13", "01.2.13", false},
		{"0.2.13", "0.2.013", false},
		{"0.2.13", "0.2.13+", false},
		{"0.2.13", "0.2.13-alpha..1", false},
		{"0.2.13", "0.2.13garbage", false},
		{"invalid", "0.2.13", false},
	} {
		for _, pair := range [][2]string{{tc.local, tc.peer}, {tc.peer, tc.local}} {
			if err := Check(pair[0], pair[1]); (err == nil) != tc.compatible {
				t.Errorf("Check(%q, %q) = %v; compatible=%v", pair[0], pair[1], err, tc.compatible)
			}
		}
	}
}
