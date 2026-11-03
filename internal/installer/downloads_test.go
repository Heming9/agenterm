package installer

import (
	"agenterm/internal/components"
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"fmt"
	"github.com/ulikunitz/xz"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

var clientNames = []string{
	"agenterm-client-cli-macos-amd64", "agenterm-client-cli-linux-amd64", "agenterm-client-cli-windows-amd64.exe",
	"agenterm-client-cli-macos-arm64", "agenterm-client-cli-linux-arm64", "agenterm-client-cli-windows-arm64.exe",
}

func sampleClients() fstest.MapFS {
	files := make(fstest.MapFS)
	for _, name := range clientNames {
		files[name] = &fstest.MapFile{Data: []byte(strings.Repeat(name+"\n", 100)), Mode: 0755}
	}
	return files
}

func TestDownloadsRoundTrip(t *testing.T) {
	source := sampleClients()
	files, err := components.BuildClientDownloads(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 13 {
		t.Fatalf("got %d downloads, want twelve archives and manifest", len(files))
	}
	for _, name := range clientNames {
		var got []byte
		gr, err := xz.NewReader(bytes.NewReader(files[strings.TrimSuffix(name, ".exe")+".tar.xz"]))
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
		if !bytes.Equal(got, source[name].Data) {
			t.Fatalf("%s changed during packing", name)
		}
	}
	lines := strings.Split(strings.TrimSpace(string(files["SHA256SUMS"])), "\n")
	if len(lines) != 6 {
		t.Fatalf("got %d checksums, want 6", len(lines))
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
	const wantManifestHash = "25f390eb73d44aea5090a8a296829d43568d0557d0ab597361cef4d3f2890561"
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
	files := components.Downloads(assets.ClientCLI(), "agenterm-client-cli")
	manifest, err := os.ReadFile(filepath.Join(directory, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".tar.xz") && !strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".zip") {
			continue
		}
		release, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		served, err := fs.ReadFile(files, name)
		if err != nil || !bytes.Equal(served, release) {
			t.Fatalf("runtime %s differs from release: %v", name, err)
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
