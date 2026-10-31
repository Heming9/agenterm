// Package installer contains the installation scripts and release download assets.
package installer

import (
	"agenterm/internal/i18n"
	"embed"
	"io/fs"
	"strings"
)

//go:embed install.sh install.ps1 assets
var content embed.FS

func Script(name string) ([]byte, error) {
	data, err := content.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return []byte(strings.ReplaceAll(string(data), "@TRANSLATIONS@", i18n.ScriptTranslations(strings.HasSuffix(name, ".ps1")))), nil
}

func Assets() fs.FS {
	assets, _ := fs.Sub(content, "assets")
	return assets
}
