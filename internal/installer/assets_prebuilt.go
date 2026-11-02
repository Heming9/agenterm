//go:build agenterm_prebuilt

package installer

import (
	"embed"
	"io/fs"
)

// Deployment builds prepare these exact release files before compiling. Keep
// the raw XZ bundle out of this build so startup does no archive compression.
//
//go:embed assets/downloads/*
var preparedDownloads embed.FS

func Assets() (fs.FS, error) {
	return fs.Sub(preparedDownloads, "assets/downloads")
}
