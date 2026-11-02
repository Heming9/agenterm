//go:build !agenterm_prebuilt

package installer

import (
	"bytes"
	_ "embed"
	"io/fs"
)

// Ordinary go builds remain self-contained with the tracked bundle.
//
//go:embed assets/clients.tar.xz
var clientBundle []byte

func Assets() (fs.FS, error) {
	return unpackDownloads(bytes.NewReader(clientBundle))
}
