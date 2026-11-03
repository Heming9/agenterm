package components

import (
	"agenterm/internal/xzarchive"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Canonical CLI targets shared by component packing and release checks.
var clientNames = []string{
	"agenterm-client-cli-macos-amd64", "agenterm-client-cli-linux-amd64", "agenterm-client-cli-windows-amd64.exe",
	"agenterm-client-cli-macos-arm64", "agenterm-client-cli-linux-arm64", "agenterm-client-cli-windows-arm64.exe",
}

// BuildClientDownloads creates release archives and source checksums.
// Release packing and on-demand downloads share the same per-file encoders.
func BuildClientDownloads(source fs.FS) (map[string][]byte, error) {
	return buildClientDownloads(func(name string) ([]byte, error) { return fs.ReadFile(source, name) })
}

func buildClientDownloads(read func(string) ([]byte, error)) (map[string][]byte, error) {
	files := make(map[string][]byte)
	hashes := make(map[string][sha256.Size]byte)
	for _, name := range clientNames {
		data, err := read(name)
		if err != nil {
			return nil, err
		}
		formats := []string{"tar.xz", "tar.gz"}
		if strings.HasSuffix(name, ".exe") {
			formats[1] = "zip"
		}
		for _, format := range formats {
			archive, compressed, err := clientArchive(name, data, format)
			if err != nil {
				return nil, err
			}
			files[archive] = compressed
		}
		hashes[name] = sha256.Sum256(data)
	}
	names := make([]string, 0, len(hashes))
	for name := range hashes {
		names = append(names, name)
	}
	sort.Strings(names)
	var manifest bytes.Buffer
	for _, name := range names {
		fmt.Fprintf(&manifest, "%x  %s\n", hashes[name], name)
	}
	files["SHA256SUMS"] = manifest.Bytes()
	return files, nil
}

func clientArchive(name string, data []byte, format string) (string, []byte, error) {
	compressed, err := xzarchive.SingleFormat(name, data, format)
	return strings.TrimSuffix(name, ".exe") + "." + format, compressed, err
}
