// Package components embeds and installs the server's complete component bundle.
package components

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

//go:generate python3 ../../scripts/build_components.py

//go:embed assets/components.zip
var bundle []byte

const maxFileBytes = 64 << 20
const maxTotalBytes = 512 << 20

type fileInfo struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type manifest struct {
	Schema int                 `json:"schema"`
	Files  map[string]fileInfo `json:"files"`
}

var clientArchive = regexp.MustCompile(`^client-cli/agenterm-client-cli-(macos|linux|windows)-(amd64|arm64)\.(tar\.gz|zip)$`)
var peerArchive = regexp.MustCompile(`^p2p-cli/p2p-cli-(macos|linux|windows)-(amd64|arm64)\.(tar\.gz|zip)$`)

func validName(name string) bool {
	if !fs.ValidPath(name) || strings.ContainsAny(name, `\:`) {
		return false
	}
	if name == "client-cli/SHA256SUMS" || name == "p2p-cli/manifest.json" {
		return true
	}
	return (clientArchive.MatchString(name) || peerArchive.MatchString(name)) &&
		(strings.Contains(name, "-windows-") == strings.HasSuffix(name, ".zip"))
}
func validateManifest(m manifest) error {
	if m.Schema != 1 || len(m.Files) < 9 || len(m.Files) > 32 {
		return errors.New("invalid components manifest")
	}
	var total int64
	for name, info := range m.Files {
		hash, err := hex.DecodeString(info.SHA256)
		if !validName(name) || info.Bytes <= 0 || info.Bytes > maxFileBytes || err != nil || len(hash) != 32 {
			return fmt.Errorf("invalid component entry %q", name)
		}
		total += info.Bytes
	}
	if total > maxTotalBytes {
		return errors.New("components bundle exceeds size limit")
	}
	for _, platform := range []string{"macos", "linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			ext := ".tar.gz"
			if platform == "windows" {
				ext = ".zip"
			}
			if _, ok := m.Files["client-cli/agenterm-client-cli-"+platform+"-"+arch+ext]; !ok {
				return errors.New("components bundle is missing a CLI platform")
			}
		}
	}
	if _, ok := m.Files["client-cli/SHA256SUMS"]; !ok {
		return errors.New("CLI checksums missing")
	}
	if _, ok := m.Files["p2p-cli/manifest.json"]; !ok {
		return errors.New("P2P manifest missing")
	}
	return nil
}

// Pack preserves the exact downloadable archive bytes. The outer ZIP stores
// already-compressed files, so startup never recompresses platform downloads.
func Pack(w io.Writer, files map[string][]byte) error {
	m := manifest{Schema: 1, Files: make(map[string]fileInfo)}
	for name, data := range files {
		hash := sha256.Sum256(data)
		m.Files[name] = fileInfo{Bytes: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}
	}
	if err := validateManifest(m); err != nil {
		return err
	}
	metadata, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	zw := zip.NewWriter(w)
	names := []string{"manifest.json"}
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		h := &zip.FileHeader{Name: name, Method: zip.Store, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)}
		h.SetMode(0644)
		out, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		data := files[name]
		if name == "manifest.json" {
			data = metadata
		}
		if _, err = out.Write(data); err != nil {
			return err
		}
	}
	return zw.Close()
}

type Paths struct{ Root string }

func (p Paths) ClientCLI() fs.FS     { return os.DirFS(filepath.Join(p.Root, "client-cli")) }
func (p Paths) P2PDirectory() string { return filepath.Join(p.Root, "p2p-cli") }

// Prepare installs into the instance's writable runtime directory. A bundle
// hash keeps old frontends' files stable during frontend replacement.
func Prepare(base string) (Paths, error) { return install(base, bundle) }

func readBundle(data []byte) (*zip.Reader, manifest, error) {
	var m manifest
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, m, err
	}
	if len(zr.File) < 10 || len(zr.File) > 33 {
		return nil, m, errors.New("invalid components ZIP entry count")
	}
	seen := map[string]bool{}
	for _, entry := range zr.File {
		if seen[entry.Name] || !entry.Mode().IsRegular() || (entry.Name != "manifest.json" && !validName(entry.Name)) {
			return nil, m, errors.New("invalid or duplicate component ZIP path")
		}
		seen[entry.Name] = true
		if entry.Name == "manifest.json" {
			if entry.UncompressedSize64 > 64<<10 {
				return nil, m, errors.New("components manifest too large")
			}
			r, err := entry.Open()
			if err != nil {
				return nil, m, err
			}
			metadata, err := io.ReadAll(io.LimitReader(r, (64<<10)+1))
			r.Close()
			if err != nil {
				return nil, m, err
			}
			decoder := json.NewDecoder(bytes.NewReader(metadata))
			decoder.DisallowUnknownFields()
			if err = decoder.Decode(&m); err != nil {
				return nil, m, err
			}
			if decoder.Decode(new(any)) != io.EOF {
				return nil, m, errors.New("trailing components manifest data")
			}
		}
	}
	if err := validateManifest(m); err != nil {
		return nil, m, err
	}
	if len(zr.File) != len(m.Files)+1 {
		return nil, m, errors.New("components ZIP does not match its manifest")
	}
	for _, entry := range zr.File {
		if entry.Name == "manifest.json" {
			continue
		}
		info, ok := m.Files[entry.Name]
		if !ok || entry.UncompressedSize64 != uint64(info.Bytes) {
			return nil, m, errors.New("component size does not match manifest")
		}
	}
	return zr, m, nil
}

func privateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return fmt.Errorf("components directory must be private: %s", path)
	}
	return nil
}
func checkCache(path string, m manifest) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid components cache directory")
	}
	for _, sub := range []string{"client-cli", "p2p-cli"} {
		info, err := os.Lstat(filepath.Join(path, sub))
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("invalid components cache subdirectory")
		}
	}
	for name, expected := range m.Files {
		file := filepath.Join(path, filepath.FromSlash(name))
		info, err := os.Lstat(file)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != expected.Bytes {
			return errors.New("invalid cached component")
		}
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, err = io.Copy(hash, f)
		f.Close()
		if err != nil || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
			return errors.New("cached component checksum mismatch")
		}
	}
	return nil
}
func install(base string, data []byte) (Paths, error) {
	zr, m, err := readBundle(data)
	if err != nil {
		return Paths{}, fmt.Errorf("components.zip: %w", err)
	}
	if err = privateDirectory(base); err != nil {
		return Paths{}, err
	}
	root := filepath.Join(base, "components")
	if err = privateDirectory(root); err != nil {
		return Paths{}, err
	}
	hash := sha256.Sum256(data)
	destination := filepath.Join(root, hex.EncodeToString(hash[:]))
	if _, err = os.Lstat(destination); err == nil {
		if err = checkCache(destination, m); err != nil {
			return Paths{}, fmt.Errorf("components cache: %w", err)
		}
		return Paths{Root: destination}, nil
	} else if !os.IsNotExist(err) {
		return Paths{}, err
	}
	stage, err := os.MkdirTemp(root, ".extract-")
	if err != nil {
		return Paths{}, err
	}
	defer os.RemoveAll(stage)
	for _, entry := range zr.File {
		path := filepath.Join(stage, filepath.FromSlash(entry.Name))
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return Paths{}, err
		}
		in, err := entry.Open()
		if err != nil {
			return Paths{}, err
		}
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			in.Close()
			return Paths{}, err
		}
		checksum := sha256.New()
		limit := int64(64 << 10)
		if entry.Name != "manifest.json" {
			limit = m.Files[entry.Name].Bytes
		}
		n, copyErr := io.Copy(io.MultiWriter(out, checksum), io.LimitReader(in, limit+1))
		closeErr := out.Close()
		in.Close()
		if copyErr != nil {
			return Paths{}, copyErr
		}
		if closeErr != nil {
			return Paths{}, closeErr
		}
		if n > limit {
			return Paths{}, errors.New("component exceeds declared size")
		}
		if entry.Name != "manifest.json" && (n != limit || hex.EncodeToString(checksum.Sum(nil)) != m.Files[entry.Name].SHA256) {
			return Paths{}, errors.New("component ZIP checksum mismatch")
		}
	}
	if err = os.Rename(stage, destination); err != nil {
		// Another frontend can finish the same immutable bundle first.
		if cachedErr := checkCache(destination, m); cachedErr != nil {
			return Paths{}, err
		}
	}
	return Paths{Root: destination}, nil
}
