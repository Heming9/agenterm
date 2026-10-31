package installer

import (
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

	"github.com/ulikunitz/xz"
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
	const wantManifestHash = "2febbbb49b2a8e86b1649e3a57bcf240ed8b9be550e1d5c44a6d82dcc6bc22d1"
	if got := fmt.Sprintf("%x", sha256.Sum256(files["SHA256SUMS"])); got != wantManifestHash {
		t.Fatalf("download encoding changed: manifest hash %s", got)
	}
}

func xzBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := xz.NewWriter(&out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestBundleRoundTrip(t *testing.T) {
	source := sampleClients()
	var raw bytes.Buffer
	if err := WriteBundle(&raw, source); err != nil {
		t.Fatal(err)
	}
	want, err := BuildDownloads(source)
	if err != nil {
		t.Fatal(err)
	}
	compressed := xzBytes(t, raw.Bytes())
	got, err := unpackDownloads(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range want {
		if !bytes.Equal(data, got[name]) {
			t.Fatalf("reconstructed %s differs from release", name)
		}
	}
	compressed[len(compressed)-1] ^= 1
	if _, err := unpackDownloads(bytes.NewReader(compressed)); err == nil {
		t.Fatal("corrupt XZ footer accepted after tar EOF")
	}
}

func TestBundleInvalidContents(t *testing.T) {
	for _, kind := range []string{"missing", "duplicate", "unknown", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			var raw bytes.Buffer
			tw := tar.NewWriter(&raw)
			for i, name := range clientNames {
				if kind == "missing" && i == 0 {
					continue
				}
				if kind == "unknown" && i == 0 {
					name = "../client"
				}
				if kind == "symlink" && i == 0 {
					if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: "other"}); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if err := writeClient(tw, name, []byte("client")); err != nil {
					t.Fatal(err)
				}
				if kind == "duplicate" && i == 0 {
					if err := writeClient(tw, name, []byte("duplicate")); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := unpackDownloads(bytes.NewReader(xzBytes(t, raw.Bytes()))); err == nil {
				t.Fatal("invalid bundle accepted")
			}
		})
	}
}

// Release CI runs this with actual binaries and the native XZ compressor,
// proving the pure Go decoder and release downloads agree before publishing.
func TestEmbeddedReleaseDownloads(t *testing.T) {
	directory := os.Getenv("AGENTERM_RELEASE_DIR")
	if directory == "" {
		t.Skip("release assets not requested")
	}
	if _, err := fs.Stat(content, "assets/clients.tar.xz"); err != nil {
		t.Fatal(err)
	}
	files, err := Assets()
	if err != nil {
		t.Fatal(err)
	}
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
