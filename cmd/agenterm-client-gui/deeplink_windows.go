package main

import (
	"fmt"
	"golang.org/x/sys/windows/registry"
	"os"
)

func registerDeepLink() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\agenterm`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err = key.SetStringValue("", "URL:Agenterm Protocol"); err != nil {
		return err
	}
	if err = key.SetStringValue("URL Protocol", ""); err != nil {
		return err
	}
	cmd, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\agenterm\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer cmd.Close()
	return cmd.SetStringValue("", fmt.Sprintf("\"%s\" \"%%1\"", exe))
}
