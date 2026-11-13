package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// The signed-in user's AppleScript app receives the URL and asks Terminal to
// launch the CLI. The link travels through a private file, not shell history.
func registerCLILink() error {
	executable, err := stableCLILinkExecutable()
	if err != nil {
		return err
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	base := filepath.Join(config, "Agenterm")
	inbox, err := linkInbox()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(inbox, 0700); err != nil {
		return err
	}
	if err = os.Chmod(inbox, 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(base, ".cli-handler-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	bundle := filepath.Join(staging, "Agenterm CLI.app")
	script := macLinkScript(executable, inbox)
	if out, err := exec.Command("/usr/bin/osacompile", "-o", bundle, "-e", script).CombinedOutput(); err != nil {
		return fmt.Errorf("compile link handler: %w: %s", err, out)
	}
	if err := configureMacLinkBundle(bundle); err != nil {
		return err
	}
	dest := filepath.Join(base, "Agenterm CLI.app")
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if err := os.Rename(bundle, dest); err != nil {
		return err
	}
	const lsregister = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
	if out, err := exec.Command(lsregister, "-f", dest).CombinedOutput(); err != nil {
		return fmt.Errorf("register link handler: %w: %s", err, out)
	}
	return nil
}

func macLinkScript(executable, inbox string) string {
	return `on open location incomingURL
    set fileTemplate to ` + strconv.Quote(filepath.Join(inbox, "agenterm-link.XXXXXXXXXX")) + `
    set filePath to do shell script "umask 077; f=$(/usr/bin/mktemp " & quoted form of fileTemplate & "); /usr/bin/printf %s " & quoted form of incomingURL & " > \"$f\"; /usr/bin/printf %s \"$f\""
    tell application "Terminal"
        activate
        do script (quoted form of ` + strconv.Quote(executable) + ` & " -link-file " & quoted form of filePath)
    end tell
end open location`
}

func configureMacLinkBundle(bundle string) error {
	plist := filepath.Join(bundle, "Contents", "Info.plist")
	for _, args := range [][]string{
		{"-replace", "CFBundleIdentifier", "-string", "io.agenterm.cli.link", plist},
		{"-insert", "CFBundleURLTypes", "-json", `[{"CFBundleURLSchemes":["agenterm-cli"]}]`, plist},
		{"-replace", "NSAppleEventsUsageDescription", "-string", "Agenterm opens Terminal to start the requested connection.", plist},
	} {
		if out, err := exec.Command("/usr/bin/plutil", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("configure link handler: %w: %s", err, out)
		}
	}
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--deep", "--sign", "-", bundle).CombinedOutput(); err != nil {
		return fmt.Errorf("sign link handler: %w: %s", err, out)
	}
	return nil
}
