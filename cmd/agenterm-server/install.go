package main

import (
	"agenterm/internal/installer"
	"crypto/sha256"
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
	base := "https://github.com/Heming9/agenterm/releases/download/" + releaseTag
	shellHash := fmt.Sprintf("%x", sha256.Sum256(installer.Shell))
	psHash := fmt.Sprintf("%x", sha256.Sum256(installer.PowerShell))
	fmt.Printf("Invitation %d (expires in 15 minutes; share privately):\n", index)
	fmt.Printf("macOS / Linux:\n(f=$(mktemp); trap 'rm -f \"$f\"' EXIT; curl -fsSL %s/install.sh >\"$f\" && [ \"$({ sha256sum \"$f\" 2>/dev/null || shasum -a 256 \"$f\"; } | cut -d' ' -f1)\" = %s ] && sh \"$f\" %s %s)\n", base, shellHash, releaseTag, invitation)
	fmt.Printf("Windows PowerShell:\n$f=Join-Path $env:TEMP ([guid]::NewGuid().ToString()+'.ps1'); try { iwr %s/install.ps1 -OutFile $f; if ((Get-FileHash $f).Hash -ne '%s') { throw 'Checksum mismatch' }; & ([scriptblock]::Create((Get-Content $f -Raw))) '%s' '%s' } finally { Remove-Item $f -ErrorAction SilentlyContinue }\n\n", base, psHash, releaseTag, invitation)
}
