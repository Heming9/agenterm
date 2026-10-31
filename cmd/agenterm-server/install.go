package main

import (
	"fmt"
	"regexp"
)

// Set by the release workflow to keep the installer and binaries on the same tag.
var releaseTag = ""
var validReleaseTag = regexp.MustCompile(`^v[0-9A-Za-z._-]+$`)

func printInstallCommands(index int, invitation string) {
	if !validReleaseTag.MatchString(releaseTag) {
		fmt.Printf("Invitation %d (expires in 15 minutes):\n%s\nInstaller commands are available in release builds.\n\n", index, invitation)
		return
	}
	shell, powershell := installCommands(releaseTag, invitation)
	fmt.Printf("Invitation %d (expires in 15 minutes; share privately):\n", index)
	fmt.Printf("macOS / Linux:\n%s\nWindows PowerShell:\n%s\n\n", shell, powershell)
}

func installCommands(tag, invitation string) (string, string) {
	base := "https://github.com/Heming9/agenterm/releases/download/" + tag
	// Command substitution leaves stdin connected to the terminal for consent.
	shell := fmt.Sprintf("s=$(curl -fsSL '%s/install.sh') && sh -c \"$s\" -- '%s'", base, invitation)
	powershell := fmt.Sprintf("&([scriptblock]::Create((irm '%s/install.ps1'))) '%s'", base, invitation)
	return shell, powershell
}
