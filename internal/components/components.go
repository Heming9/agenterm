// Package components embeds and installs the server's complete component bundle.
package components

import (
	"agenterm/internal/p2p"
	"agenterm/internal/xzarchive"
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
)

//go:generate python3 ../../scripts/build_components.py

//go:embed assets/components.tar.xz
var bundle []byte

const maxFileBytes = xzarchive.MaxFileBytes
const maxTotalBytes = xzarchive.MaxTotalBytes

type fileInfo struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type manifest struct {
	Schema     int                 `json:"schema"`
	P2PVersion string              `json:"p2p_version"`
	Files      map[string]fileInfo `json:"files"`
}

var rawName = regexp.MustCompile(`^(client-cli/agenterm-client-cli|p2p-cli/p2p-cli)-(macos|linux|windows)-(amd64|arm64)(\.exe)?$`)
var versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)

func describe(files map[string][]byte) map[string]fileInfo {
	result := make(map[string]fileInfo, len(files))
	for name, data := range files {
		hash := sha256.Sum256(data)
		result[name] = fileInfo{Bytes: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}
	}
	return result
}
func validateManifest(m manifest) error {
	if m.Schema != 2 || !versionPattern.MatchString(m.P2PVersion) || len(m.Files) != 12 {
		return errors.New("invalid components manifest")
	}
	var total int64
	for name, info := range m.Files {
		hash, err := hex.DecodeString(info.SHA256)
		if !rawName.MatchString(name) || strings.Contains(name, "-windows-") != strings.HasSuffix(name, ".exe") || info.Bytes <= 0 || info.Bytes > maxFileBytes || err != nil || len(hash) != 32 {
			return fmt.Errorf("invalid component entry %q", name)
		}
		total += info.Bytes
	}
	if total > maxTotalBytes {
		return errors.New("components bundle exceeds size limit")
	}
	// The name pattern admits exactly twelve OS/architecture/component combinations.
	return nil
}

func sourceMetadata(m manifest) (map[string][]byte, error) {
	files := map[string][]byte{}
	names := make([]string, 0, 6)
	for name := range m.Files {
		if strings.HasPrefix(name, "client-cli/") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var checksums bytes.Buffer
	for _, name := range names {
		fmt.Fprintf(&checksums, "%s  %s\n", m.Files[name].SHA256, strings.TrimPrefix(name, "client-cli/"))
	}
	files["client-cli/SHA256SUMS"] = checksums.Bytes()
	peers := p2p.Manifest{Version: m.P2PVersion}
	for _, platform := range []string{"darwin", "linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			name, _ := p2p.Filename(platform, arch)
			peers.Artifacts = append(peers.Artifacts, p2p.Artifact{Platform: platform, Arch: arch, File: name, SHA256: m.Files["p2p-cli/"+name].SHA256})
		}
	}
	data, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		return nil, err
	}
	files["p2p-cli/manifest.json"] = append(data, '\n')
	return files, nil
}

// Pack stores raw executables in one solid tar.xz; no download archives are nested.
func Pack(w io.Writer, raw map[string][]byte, version string) error {
	m := manifest{Schema: 2, P2PVersion: version, Files: describe(raw)}
	if err := validateManifest(m); err != nil {
		return err
	}
	metadata, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	entries := make(map[string][]byte, len(raw)+1)
	for name, data := range raw {
		entries[name] = data
	}
	entries["manifest.json"] = metadata
	return xzarchive.Write(w, entries)
}

type Paths struct{ Root string }

func (p Paths) ClientCLI() fs.FS         { return os.DirFS(filepath.Join(p.Root, "client-cli")) }
func (p Paths) P2PDirectory() string     { return filepath.Join(p.Root, "p2p-cli") }
func Prepare(base string) (Paths, error) { return install(base, bundle) }

// PrepareBundle also lets the release/build helper use the runtime encoder.
func PrepareBundle(base string, data []byte) (Paths, error) { return install(base, data) }

func readBundle(data []byte) (map[string][]byte, manifest, error) {
	var m manifest
	files, err := xzarchive.Read(data)
	if err != nil {
		return nil, m, err
	}
	metadata := files["manifest.json"]
	if len(metadata) == 0 || len(metadata) > 64<<10 {
		return nil, m, errors.New("invalid components manifest size")
	}
	decoder := json.NewDecoder(bytes.NewReader(metadata))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return nil, m, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, m, errors.New("trailing components manifest data")
	}
	if err := validateManifest(m); err != nil {
		return nil, m, err
	}
	if len(files) != len(m.Files)+1 {
		return nil, m, errors.New("components tar does not match manifest")
	}
	delete(files, "manifest.json")
	for name, data := range files {
		expected, ok := m.Files[name]
		hash := sha256.Sum256(data)
		if !ok || int64(len(data)) != expected.Bytes || hex.EncodeToString(hash[:]) != expected.SHA256 {
			return nil, m, errors.New("component checksum or size mismatch")
		}
	}
	return files, m, nil
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
	metadata, err := sourceMetadata(m)
	if err != nil {
		return err
	}
	for name, expected := range metadata {
		file := filepath.Join(path, filepath.FromSlash(name))
		info, err := os.Lstat(file)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("invalid cached component metadata")
		}
		data, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(data, expected) {
			return errors.New("cached component metadata mismatch")
		}
	}
	return nil
}
func install(base string, data []byte) (Paths, error) {
	raw, m, err := readBundle(data)
	if err != nil {
		return Paths{}, fmt.Errorf("components.tar.xz: %w", err)
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
	files, err := sourceMetadata(m)
	if err != nil {
		return Paths{}, err
	}
	for name, content := range raw {
		files[name] = content
	}
	stage, err := os.MkdirTemp(root, ".extract-")
	if err != nil {
		return Paths{}, err
	}
	defer os.RemoveAll(stage)
	for name, content := range files {
		path := filepath.Join(stage, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return Paths{}, err
		}
		if err = os.WriteFile(path, content, 0600); err != nil {
			return Paths{}, err
		}
	}
	if err = os.Rename(stage, destination); err != nil {
		if cachedErr := checkCache(destination, m); cachedErr != nil {
			return Paths{}, err
		}
	}
	return Paths{Root: destination}, nil
}
