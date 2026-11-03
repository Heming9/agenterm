package installer

import (
	"agenterm/internal/components"
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
	"strings"
	"testing"
	"testing/fstest"
)

func sampleClients() fstest.MapFS {
	files := make(fstest.MapFS)
	for _, name := range clientNames {
		files[name] = &fstest.MapFile{Data: []byte(strings.Repeat(name+"\n", 100)), Mode: 0755}
	}
	return files
}

func TestDownloadsRoundTrip(t *testing.T) {
	source := sampleClients()
	files, err := BuildDownloads(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 7 {
		t.Fatalf("got %d downloads, want six archives and manifest", len(files))
	}
	for _, name := range clientNames {
		var got []byte
		if strings.HasSuffix(name, ".exe") {
			data := files[strings.TrimSuffix(name, ".exe")+".zip"]
			zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if len(zr.File) != 1 || zr.File[0].Name != name || zr.File[0].Method != zip.Deflate {
				t.Fatalf("unexpected ZIP contents for %s", name)
			}
			r, err := zr.File[0].Open()
			if err != nil {
				t.Fatal(err)
			}
			got, err = io.ReadAll(r)
			r.Close()
			if err != nil {
				t.Fatal(err)
			}
		} else {
			gr, err := gzip.NewReader(bytes.NewReader(files[name+".tar.gz"]))
			if err != nil {
				t.Fatal(err)
			}
			tr := tar.NewReader(gr)
			h, err := tr.Next()
			if err != nil {
				t.Fatal(err)
			}
			if h.Name != name || h.Mode != 0755 || h.Typeflag != tar.TypeReg {
				t.Fatalf("unexpected tar header: %+v", h)
			}
			got, err = io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tr.Next(); err != io.EOF {
				t.Fatalf("extra tar entry: %v", err)
			}
			if _, err := io.Copy(io.Discard, gr); err != nil {
				t.Fatal(err)
			}
			gr.Close()
		}
		if !bytes.Equal(got, source[name].Data) {
			t.Fatalf("%s changed during packing", name)
		}
	}
	lines := strings.Split(strings.TrimSpace(string(files["SHA256SUMS"])), "\n")
	if len(lines) != 12 {
		t.Fatalf("got %d checksums, want 12", len(lines))
	}
	for _, line := range lines {
		hash, name, ok := strings.Cut(line, "  ")
		data := files[name]
		if raw, exists := source[name]; exists {
			data = raw.Data
		}
		if !ok || data == nil || hash != fmt.Sprintf("%x", sha256.Sum256(data)) {
			t.Fatalf("incorrect checksum: %s", line)
		}
	}
	// A fixed digest also runs on Windows CI to catch host-dependent metadata.
	const wantManifestHash = "3edcfda1d392ccd8a297b1059ddd1c2296a35c88629bc4d4fba730d5459b4232"
	if got := fmt.Sprintf("%x", sha256.Sum256(files["SHA256SUMS"])); got != wantManifestHash {
		t.Fatalf("download encoding changed: manifest hash %s", got)
	}
}

// Release CI verifies the extracted unified bundle against public downloads.
func TestEmbeddedReleaseDownloads(t *testing.T) {
	directory := os.Getenv("AGENTERM_RELEASE_DIR")
	if directory == "" {
		t.Skip("release assets not requested")
	}
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	assets, err := components.Prepare(base)
	if err != nil {
		t.Fatal(err)
	}
	files := assets.ClientCLI()
	manifest, err := os.ReadFile(filepath.Join(directory, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		_, name, _ := strings.Cut(line, "  ")
		if !strings.HasSuffix(name, ".zip") && !strings.HasSuffix(name, ".tar.gz") {
			continue
		}
		release, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		served, err := fs.ReadFile(files, name)
		if err != nil || !bytes.Equal(served, release) {
			t.Fatalf("embedded %s differs from release: %v", name, err)
		}
	}
	served, err := fs.ReadFile(files, "SHA256SUMS")
	if err != nil || !bytes.Equal(served, manifest) {
		t.Fatalf("embedded manifest differs from release: %v", err)
	}
}

func BenchmarkAssets(b *testing.B) {
	base := b.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		assets, err := components.Prepare(base)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := fs.ReadFile(assets.ClientCLI(), "SHA256SUMS"); err != nil {
			b.Fatal(err)
		}
	}
}
