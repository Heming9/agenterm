package components

import (
	"agenterm/internal/xzarchive"
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
)

func unpackDownload(t *testing.T, data []byte, format, member string) []byte {
	t.Helper()
	if format == "tar.xz" {
		files, err := xzarchive.Read(data)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 {
			t.Fatal("unexpected members")
		}
		return files[member]
	}
	if format == "zip" {
		z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		if len(z.File) != 1 || z.File[0].Name != member {
			t.Fatal("unexpected ZIP member")
		}
		r, err := z.File[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		b, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	h, err := tr.Next()
	if err != nil || h.Name != member {
		t.Fatal("unexpected gzip member", err)
	}
	b, err := io.ReadAll(tr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tr.Next(); err != io.EOF {
		t.Fatal("extra gzip member", err)
	}
	if _, err = io.Copy(io.Discard, gz); err != nil {
		t.Fatal(err)
	}
	return b
}
func TestDownloadsOfferCompressedAndRawSourcesWithoutWritingArchives(t *testing.T) {
	for _, component := range []string{"agenterm-client-cli", "p2p-cli"} {
		for _, platform := range []string{"linux", "windows"} {
			t.Run(component+"/"+platform, func(t *testing.T) {
				root := t.TempDir()
				base := component + "-" + platform + "-arm64"
				name := base
				legacy := "tar.gz"
				if platform == "windows" {
					name += ".exe"
					legacy = "zip"
				}
				raw := []byte("native executable bytes\n")
				if err := os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
					t.Fatal(err)
				}
				downloads := Downloads(os.DirFS(root), component)
				original, err := fs.ReadFile(downloads, name)
				if err != nil || !bytes.Equal(original, raw) {
					t.Fatal("raw download changed", err)
				}
				for _, format := range []string{"tar.xz", legacy} {
					file := base + "." + format
					b, err := fs.ReadFile(downloads, file)
					if err != nil {
						t.Fatal(err)
					}
					member := name
					if component == "p2p-cli" {
						member = "p2p-cli"
						if platform == "windows" {
							member += ".exe"
						}
					}
					if !bytes.Equal(unpackDownload(t, b, format, member), raw) {
						t.Fatal("archive changed source")
					}
					hash, err := fs.ReadFile(downloads, file+".sha256")
					if err != nil || string(hash) != fmt.Sprintf("%x\n", sha256.Sum256(b)) {
						t.Fatal("incorrect archive checksum", err)
					}
					again, err := fs.ReadFile(downloads, file)
					if err != nil || !bytes.Equal(again, b) {
						t.Fatal("cached archive changed", err)
					}
				}
				files, err := os.ReadDir(root)
				if err != nil || len(files) != 1 || files[0].Name() != name {
					t.Fatal("download persisted derived archives", err)
				}
				for _, bad := range []string{"../" + name, `..\outside`, `C:\outside`, "other-component-linux-arm64.tar.xz", base + ".7z", name + ".sha256.sha256"} {
					if _, err := fs.ReadFile(downloads, bad); err == nil {
						t.Fatalf("invalid download accepted: %s", bad)
					}
				}
			})
		}
	}
}

type countedFS struct {
	fs.FS
	mu    sync.Mutex
	reads int
	name  string
}

func (f *countedFS) Open(name string) (fs.File, error) {
	if name == f.name {
		f.mu.Lock()
		f.reads++
		f.mu.Unlock()
	}
	return f.FS.Open(name)
}
func TestParallelDownloadRequestsPackOnce(t *testing.T) {
	source := &countedFS{FS: fstest.MapFS{"p2p-cli-linux-amd64": &fstest.MapFile{Data: bytes.Repeat([]byte("binary"), 1000)}}, name: "p2p-cli-linux-amd64"}
	downloads := Downloads(source, "p2p-cli")
	var group sync.WaitGroup
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := fs.ReadFile(downloads, "p2p-cli-linux-amd64.tar.xz"); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.reads != 1 {
		t.Fatalf("packed %d times for concurrent requests", source.reads)
	}
}
