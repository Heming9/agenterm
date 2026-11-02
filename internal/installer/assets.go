// Package installer contains the installation scripts and release download assets.
package installer

import (
	"agenterm/internal/i18n"
	"embed"
	"strings"
)

//go:generate python3 ../../scripts/build_installer_assets.py

//go:embed install.sh install.ps1
var content embed.FS

func Script(name string) ([]byte, error) {
	data, err := content.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return []byte(strings.ReplaceAll(string(data), "@TRANSLATIONS@", i18n.ScriptTranslations(strings.HasSuffix(name, ".ps1")))), nil
}
