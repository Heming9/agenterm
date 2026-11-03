package installer

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// Canonical CLI targets shared by component packing and release checks.
var clientNames = []string{
	"agenterm-client-cli-macos-amd64", "agenterm-client-cli-linux-amd64", "agenterm-client-cli-windows-amd64.exe",
	"agenterm-client-cli-macos-arm64", "agenterm-client-cli-linux-arm64", "agenterm-client-cli-windows-arm64.exe",
}

// BuildDownloads creates the exact CLI archives stored in components.zip and
// published by releases. Fixed metadata and standard compressors keep output
// deterministic with the same Go toolchain; the server serves these bytes.
func BuildDownloads(source fs.FS) (map[string][]byte, error) {
	files := make(map[string][]byte)
	hashes := make(map[string][sha256.Size]byte)
	for _, name := range clientNames {
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return nil, err
		}
		archive, compressed, err := clientArchive(name, data)
		if err != nil {
			return nil, err
		}
		files[archive] = compressed
		hashes[name] = sha256.Sum256(data)
		hashes[archive] = sha256.Sum256(compressed)
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

func clientArchive(name string, data []byte) (string, []byte, error) {
	var out bytes.Buffer
	if strings.HasSuffix(name, ".exe") {
		zw := zip.NewWriter(&out)
		zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
			return flate.NewWriter(w, flate.BestCompression)
		})
		h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)}
		h.SetMode(0755)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return "", nil, err
		}
		if _, err := w.Write(data); err != nil {
			return "", nil, err
		}
		if err := zw.Close(); err != nil {
			return "", nil, err
		}
		return strings.TrimSuffix(name, ".exe") + ".zip", out.Bytes(), nil
	}
	gw, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if err != nil {
		return "", nil, err
	}
	tw := tar.NewWriter(gw)
	if err := writeClient(tw, name, data); err != nil {
		return "", nil, err
	}
	if err := tw.Close(); err != nil {
		return "", nil, err
	}
	if err := gw.Close(); err != nil {
		return "", nil, err
	}
	return name + ".tar.gz", out.Bytes(), nil
}

func writeClient(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}
