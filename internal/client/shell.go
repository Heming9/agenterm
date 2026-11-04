package client

import (
	"path/filepath"
	"strings"
	"sync"
)

var shellOnce sync.Once
var selectedShell string

// Shell reports the shell selected for commands in this client process.
func Shell() string { return persistentShellPath() }

func getShell() string {
	shellOnce.Do(func() {
		selectedShell = defaultShell()
		if path := parentShellPath(); path != "" {
			selectedShell = path
		}
	})
	return selectedShell
}

func isSupportedShell(path string) bool {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
	switch name {
	case "cmd", "powershell", "pwsh", "sh", "bash", "zsh", "dash", "ksh", "fish", "ash":
		return true
	default:
		return false
	}
}
