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

	"github.com/ulikunitz/xz"
)

// Group by architecture so the solid archive can reuse similar machine code.
var clientNames = []string{
	"agenterm-client-cli-macos-amd64", "agenterm-client-cli-linux-amd64", "agenterm-client-cli-windows-amd64.exe",
	"agenterm-client-cli-macos-arm64", "agenterm-client-cli-linux-arm64", "agenterm-client-cli-windows-arm64.exe",
}

// DownloadFiles is immutable after construction. Each Open gets its own seek
// cursor so concurrent HTTP downloads, HEAD and Range requests work normally.
type DownloadFiles map[string][]byte

func (files DownloadFiles) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	data, ok := files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &downloadFile{Reader: bytes.NewReader(data), name: name}, nil
}

type downloadFile struct {
	*bytes.Reader
	name string
}

func (f *downloadFile) Close() error               { return nil }
func (f *downloadFile) Stat() (fs.FileInfo, error) { return f, nil }
func (f *downloadFile) Name() string               { return f.name }
func (f *downloadFile) Size() int64                { return f.Reader.Size() }
func (f *downloadFile) Mode() fs.FileMode          { return 0444 }
func (f *downloadFile) ModTime() time.Time         { return time.Time{} }
func (f *downloadFile) IsDir() bool                { return false }
func (f *downloadFile) Sys() any                   { return nil }

// BuildDownloads is shared by the release tool and server. Fixed metadata and
// Go's standard compressors make their archives and checksums byte-identical
// when built with the same Go toolchain. Never recompress only one copy in CI.
func BuildDownloads(source fs.FS) (DownloadFiles, error) {
	files := make(DownloadFiles)
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

// WriteBundle writes only the six executable files. Release CI applies XZ to
// this tar stream; no external compressor is needed by the running server.
func WriteBundle(w io.Writer, source fs.FS) error {
	tw := tar.NewWriter(w)
	for _, name := range clientNames {
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		if err := writeClient(tw, name, data); err != nil {
			return err
		}
	}
	return tw.Close()
}

func unpackDownloads(r io.Reader) (DownloadFiles, error) {
	xr, err := xz.NewReader(r)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(xr)
	clients := make(DownloadFiles)
	allowed := make(map[string]bool)
	for _, name := range clientNames {
		allowed[name] = true
	}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if !allowed[h.Name] || h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > 64<<20 {
			return nil, fmt.Errorf("invalid bundled client %q", h.Name)
		}
		delete(allowed, h.Name)
		clients[h.Name], err = io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
	}
	// tar EOF precedes the XZ footer. Read to the end to validate its checksum.
	if _, err := io.Copy(io.Discard, xr); err != nil {
		return nil, err
	}
	if len(allowed) != 0 {
		return nil, fmt.Errorf("client bundle is missing %d platforms", len(allowed))
	}
	return BuildDownloads(clients)
}
