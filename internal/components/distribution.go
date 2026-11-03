package components

import (
	"agenterm/internal/xzarchive"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

var archiveName = regexp.MustCompile(`^(agenterm-client-cli|p2p-cli)-(macos|linux|windows)-(amd64|arm64)\.(tar\.xz|tar\.gz|zip)$`)

// Downloads exposes original executables and packs only requested formats.
// Each frontend keeps a bounded memory cache; no derived archives are installed.
func Downloads(source fs.FS, component string) fs.FS {
	return &distribution{source: source, component: component, cache: map[string][]byte{}}
}

type distribution struct {
	source    fs.FS
	component string
	mu        sync.Mutex
	cache     map[string][]byte
	bytes     int
	group     singleflight.Group
}

func (d *distribution) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) || strings.ContainsAny(name, `/\:`) || strings.HasSuffix(name, ".sha256.sha256") {
		return nil, fs.ErrNotExist
	}
	if file, err := d.source.Open(name); err == nil {
		return file, nil
	}
	d.mu.Lock()
	b, ok := d.cache[name]
	d.mu.Unlock()
	if !ok {
		value, err, _ := d.group.Do(name, func() (any, error) {
			d.mu.Lock()
			b, ok := d.cache[name]
			d.mu.Unlock()
			if ok {
				return b, nil
			}
			if target, checksum := strings.CutSuffix(name, ".sha256"); checksum {
				file, err := d.Open(target)
				if err != nil {
					return nil, err
				}
				defer file.Close()
				hash := sha256.New()
				if _, err = io.Copy(hash, io.LimitReader(file, maxFileBytes+(128<<10))); err != nil {
					return nil, err
				}
				b = []byte(fmt.Sprintf("%x\n", hash.Sum(nil)))
			} else {
				match := archiveName.FindStringSubmatch(name)
				if match == nil || match[1] != d.component || (match[4] == "zip" && match[2] != "windows") || (match[4] == "tar.gz" && match[2] == "windows") {
					return nil, fs.ErrNotExist
				}
				rawName := strings.TrimSuffix(name, "."+match[4])
				if match[2] == "windows" {
					rawName += ".exe"
				}
				raw, err := fs.ReadFile(d.source, rawName)
				if err != nil {
					return nil, err
				}
				if len(raw) > maxFileBytes {
					return nil, fmt.Errorf("component exceeds size limit")
				}
				member := rawName
				if d.component == "p2p-cli" {
					member = "p2p-cli"
					if match[2] == "windows" {
						member += ".exe"
					}
				}
				b, err = xzarchive.SingleFormat(member, raw, match[4])
				if err != nil {
					return nil, err
				}
				if d.component == "p2p-cli" && match[4] == "tar.xz" && match[2] != "linux" && len(b) > 2<<20 {
					return nil, fmt.Errorf("p2p-cli XZ download exceeds 2 MiB")
				}
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			if d.bytes+len(b) > 128<<20 {
				d.cache = map[string][]byte{}
				d.bytes = 0
			}
			d.cache[name] = b
			d.bytes += len(b)
			return b, nil
		})
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		b = value.([]byte)
	}
	return &memoryFile{Reader: bytes.NewReader(b), name: name}, nil
}

type memoryFile struct {
	*bytes.Reader
	name string
}

func (f *memoryFile) Close() error { return nil }
func (f *memoryFile) Stat() (fs.FileInfo, error) {
	return fileInfoValue{name: f.name, size: f.Size()}, nil
}

type fileInfoValue struct {
	name string
	size int64
}

func (f fileInfoValue) Name() string       { return f.name }
func (f fileInfoValue) Size() int64        { return f.size }
func (f fileInfoValue) Mode() fs.FileMode  { return 0600 }
func (f fileInfoValue) ModTime() time.Time { return time.Time{} }
func (f fileInfoValue) IsDir() bool        { return false }
func (f fileInfoValue) Sys() any           { return nil }
