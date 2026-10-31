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
	shellHash := fmt.Sprintf("%x", sha256.Sum256(installer.Shell))
	psHash := fmt.Sprintf("%x", sha256.Sum256(installer.PowerShell))
	base := "https://github.com/Heming9/agenterm/releases/download/" + releaseTag
	fmt.Printf("Invitation %d (expires in 15 minutes; share privately):\n", index)
	fmt.Printf("macOS / Linux:\ntmp=$(mktemp); curl -fsSL '%s/install.sh' -o \"$tmp\" && { if command -v shasum >/dev/null 2>&1; then shasum -a 256 \"$tmp\"; else sha256sum \"$tmp\"; fi; } | { read -r hash rest; [ \"$hash\" = '%s' ]; } && sh \"$tmp\" '%s' '%s'; rm -f \"$tmp\"\n", base, shellHash, releaseTag, invitation)
	fmt.Printf("Windows PowerShell:\n$p=Join-Path $env:TEMP ([guid]::NewGuid().ToString()+'.ps1'); try { Invoke-WebRequest '%s/install.ps1' -OutFile $p; if ((Get-FileHash -Algorithm SHA256 $p).Hash -ne '%s') { throw 'Installer checksum mismatch' }; powershell.exe -NoProfile -ExecutionPolicy Bypass -File $p -Version '%s' -Invitation '%s' } finally { Remove-Item $p -ErrorAction SilentlyContinue }\n\n", base, psHash, releaseTag, invitation)
}
