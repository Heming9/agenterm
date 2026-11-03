package installer

import (
	"agenterm/internal/components"
	"io/fs"
)

// BuildDownloads uses the same encoding as on-demand component downloads.
func BuildDownloads(source fs.FS) (map[string][]byte, error) {
	return components.BuildClientDownloads(source)
}
