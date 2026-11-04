package client

import "testing"

func TestSupportedShell(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"bash", true}, {"/bin/zsh", true}, {"fish", true},
		{"pwsh.exe", true}, {"PowerShell.EXE", true}, {"cmd.exe", true},
		{"WindowsTerminal.exe", false}, {"", false},
	} {
		if got := isSupportedShell(tc.name); got != tc.want {
			t.Errorf("isSupportedShell(%q)=%t, want %t", tc.name, got, tc.want)
		}
	}
}
