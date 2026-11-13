package main

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

func registerCLILink() error {
	exe, err := stableCLILinkExecutable()
	if err != nil {
		return err
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\agenterm-cli`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err = key.SetStringValue("", "URL:Agenterm CLI Protocol"); err != nil {
		return err
	}
	if err = key.SetStringValue("URL Protocol", ""); err != nil {
		return err
	}
	cmd, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\agenterm-cli\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer cmd.Close()
	return cmd.SetStringValue("", fmt.Sprintf("\"%s\" \"%%1\"", exe))
}
