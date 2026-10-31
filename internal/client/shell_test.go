package client

import "testing"

func TestShellFlag(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"bash", "-c"}, {"/bin/zsh", "-c"}, {"fish", "-c"},
		{"pwsh.exe", "-Command"}, {"PowerShell.EXE", "-Command"}, {"cmd.exe", "/C"},
		{"WindowsTerminal.exe", ""}, {"", ""},
	} {
		if got := shellFlag(tc.name); got != tc.want {
			t.Errorf("shellFlag(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}
