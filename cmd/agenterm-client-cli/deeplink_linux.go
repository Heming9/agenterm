package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func registerCLILink() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		data = filepath.Join(home, ".local", "share")
	}
	if !filepath.IsAbs(data) {
		return fmt.Errorf("XDG_DATA_HOME must be absolute")
	}
	dir := filepath.Join(data, "applications")
	if err = os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// Desktop Exec syntax requires escaping backslashes, quotes, backticks and dollar signs.
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", "\\$", "%", "%%").Replace(exe)
	content := "[Desktop Entry]\nType=Application\nName=Agenterm CLI\nExec=\"" + escaped + "\" %u\nTerminal=true\nNoDisplay=true\nMimeType=x-scheme-handler/agenterm-cli;\n"
	if err = os.WriteFile(filepath.Join(dir, "agenterm-cli.desktop"), []byte(content), 0644); err != nil {
		return err
	}
	return exec.Command("xdg-mime", "default", "agenterm-cli.desktop", "x-scheme-handler/agenterm-cli").Run()
}
