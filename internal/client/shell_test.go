package client

import (
	"reflect"
	"testing"
)

func TestShellFlag(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"bash", "-c"}, {"/bin/zsh", "-c"}, {"fish", "-c"},
		{"pwsh.exe", "-c"}, {"PowerShell.EXE", "-c"}, {"cmd.exe", "/C"},
		{"WindowsTerminal.exe", ""}, {"", ""},
	} {
		if got := shellFlag(tc.name); got != tc.want {
			t.Errorf("shellFlag(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestShellArgsSkipPowerShellProfile(t *testing.T) {
	for _, tc := range []struct {
		shell commandShell
		want  []string
	}{
		{commandShell{"powershell.exe", "-c"}, []string{"-NoP", "-c", "whoami"}},
		{commandShell{"pwsh", "-c"}, []string{"-NoP", "-c", "whoami"}},
		{commandShell{"cmd.exe", "/C"}, []string{"/C", "whoami"}},
	} {
		if got := shellArgs(tc.shell, "whoami"); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("shellArgs(%q) = %v, want %v", tc.shell.path, got, tc.want)
		}
	}
}
