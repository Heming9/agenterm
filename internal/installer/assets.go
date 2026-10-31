// Package installer contains the installation scripts and release download assets.
package installer

import (
	"embed"
	"io/fs"
)

//go:embed install.sh install.ps1 assets
var content embed.FS

func Script(name string) ([]byte, error) { return content.ReadFile(name) }

func Assets() fs.FS {
	assets, _ := fs.Sub(content, "assets")
	return assets
}
