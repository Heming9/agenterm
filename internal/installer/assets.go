// Package installer contains the installation scripts and release download assets.
package installer

import (
	"agenterm/internal/i18n"
	"embed"
	"errors"
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

func Assets() (fs.FS, error) {
	assets, _ := fs.Sub(content, "assets")
	bundle, err := assets.Open("clients.tar.xz")
	if errors.Is(err, fs.ErrNotExist) {
		return assets, nil // Source builds have no bundled downloads.
	}
	if err != nil {
		return nil, err
	}
	defer bundle.Close()
	return unpackDownloads(bundle)
}
