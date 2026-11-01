// Package installer contains the installation scripts and release download assets.
package installer

import (
	"agenterm/internal/i18n"
	"embed"
	"io/fs"
	"strings"
)

//go:generate python3 ../../scripts/build_installer_assets.py

// Embed the tracked bundle explicitly so missing resources fail the build
// instead of producing a server without working invitation installers.
//
//go:embed install.sh install.ps1 assets/clients.tar.xz
var content embed.FS

func Script(name string) ([]byte, error) {
	data, err := content.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return []byte(strings.ReplaceAll(string(data), "@TRANSLATIONS@", i18n.ScriptTranslations(strings.HasSuffix(name, ".ps1")))), nil
}

func Assets() (fs.FS, error) {
	assets, _ := fs.Sub(content, "assets")
	bundle, err := assets.Open("clients.tar.xz")
	if err != nil {
		return nil, err
	}
	defer bundle.Close()
	return unpackDownloads(bundle)
}
