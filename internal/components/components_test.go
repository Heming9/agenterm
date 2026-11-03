package components

import (
	"agenterm/internal/p2p"
	"agenterm/internal/xzarchive"
	"archive/tar"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ulikunitz/xz"
)

func TestEmbeddedP2PDistributionCoversAllPlatforms(t *testing.T) {
	paths, err := Prepare(privateBase(t))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := p2p.Load(paths.P2PDirectory())
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"darwin", "linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			if !manifest.Supports(platform, arch) {
				t.Errorf("missing %s/%s", platform, arch)
			}
		}
	}
}
func fixture() map[string][]byte {
	files := map[string][]byte{}
	for _, platform := range []string{"macos", "linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			extension := ""
			if platform == "windows" {
				extension = ".exe"
			}
			for _, component := range []string{"client-cli/agenterm-client-cli", "p2p-cli/p2p-cli"} {
				name := component + "-" + platform + "-" + arch + extension
				files[name] = []byte("binary for " + name)
			}
		}
	}
	return files
}
func packed(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Pack(&out, files, "0.1.0"); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func privateBase(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestBundleInstallReuseAndUpgrade(t *testing.T) {
	files := fixture()
	data := packed(t, files)
	if !bytes.Equal(data, packed(t, files)) {
		t.Fatal("non-deterministic bundle")
	}
	contents, _, err := readBundle(data)
	if err != nil || !bytes.Equal(contents["client-cli/agenterm-client-cli-linux-amd64"], files["client-cli/agenterm-client-cli-linux-amd64"]) {
		t.Fatalf("bundle must hold raw executables: %v", err)
	}
	base := privateBase(t)
	paths, err := install(base, data)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(paths.Root) != filepath.Join(base, "components") {
		t.Fatal(paths.Root)
	}
	cli, err := fs.ReadFile(Downloads(paths.ClientCLI(), "agenterm-client-cli"), "agenterm-client-cli-windows-arm64.tar.xz")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := xzarchive.Read(cli)
	if err != nil || !bytes.Equal(decoded["agenterm-client-cli-windows-arm64.exe"], files["client-cli/agenterm-client-cli-windows-arm64.exe"]) {
		t.Fatalf("invalid generated download: %v", err)
	}
	file := filepath.Join(paths.P2PDirectory(), "p2p-cli-macos-arm64")
	old, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(123456, 0)
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	reused, err := install(base, data)
	if err != nil || reused.Root != paths.Root {
		t.Fatal(err)
	}
	info, _ := os.Stat(file)
	if !info.ModTime().Equal(stamp) {
		t.Fatal("valid cache rewritten")
	}
	files["p2p-cli/p2p-cli-macos-arm64"] = []byte("new native binary")
	updated, err := install(base, packed(t, files))
	if err != nil || updated.Root == paths.Root {
		t.Fatal(err)
	}
	original, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(original, old) {
		t.Fatal("upgrade changed old frontend assets")
	}
}
func TestConcurrentInstallPublishesOneCompleteBundle(t *testing.T) {
	base, data := privateBase(t), packed(t, fixture())
	results := make(chan error, 12)
	var group sync.WaitGroup
	for range 12 {
		group.Add(1)
		go func() { defer group.Done(); _, err := install(base, data); results <- err }()
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(base, "components"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("partial installation: %v %v", entries, err)
	}
}
func TestCachedTamperingAndDirectorySymlinksAreRejected(t *testing.T) {
	data := packed(t, fixture())
	base := privateBase(t)
	paths, err := install(base, data)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(paths.P2PDirectory(), "p2p-cli-macos-arm64"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = install(base, data); err == nil {
		t.Fatal("tampered cache accepted")
	}
	if runtime.GOOS == "windows" {
		return
	}
	other, outside := privateBase(t), t.TempDir()
	if err = os.Symlink(outside, filepath.Join(other, "components")); err != nil {
		t.Fatal(err)
	}
	if _, err = install(other, data); err == nil {
		t.Fatal("symlink extraction root accepted")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("wrote through symlink")
	}
}
func TestInvalidBundlesNeverPublishPartialFiles(t *testing.T) {
	data := packed(t, fixture())
	for _, kind := range []string{"traversal", "backslash", "duplicate", "missing", "symlink", "hash", "oversize", "trailing-manifest", "checksum", "truncated", "trailing-stream"} {
		t.Run(kind, func(t *testing.T) {
			bad := rewriteBundle(t, data, kind)
			base := privateBase(t)
			if _, err := install(base, bad); err == nil {
				t.Fatal("invalid bundle accepted")
			}
			entries, _ := os.ReadDir(filepath.Join(base, "components"))
			if len(entries) != 0 {
				t.Fatal("failed extraction left files")
			}
		})
	}
}
func rewriteBundle(t *testing.T, data []byte, kind string) []byte {
	t.Helper()
	if kind == "truncated" {
		return data[:len(data)-5]
	}
	if kind == "checksum" {
		bad := bytes.Clone(data)
		bad[len(bad)/2] ^= 1
		return bad
	}
	if kind == "trailing-stream" {
		return append(bytes.Clone(data), data...)
	}
	files, err := xzarchive.Read(data)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	zw, err := xz.NewWriter(&out)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	for name, payload := range files {
		h := &tar.Header{Name: name, Size: int64(len(payload)), Mode: 0755, Typeflag: tar.TypeReg}
		if name == "manifest.json" {
			switch kind {
			case "trailing-manifest":
				payload = append(payload, []byte(" {}")...)
			case "oversize":
				var m manifest
				if err = json.Unmarshal(payload, &m); err != nil {
					t.Fatal(err)
				}
				v := m.Files["p2p-cli/p2p-cli-macos-arm64"]
				v.Bytes = maxFileBytes + 1
				m.Files["p2p-cli/p2p-cli-macos-arm64"] = v
				payload, _ = json.Marshal(m)
			}
		}
		if name == "p2p-cli/p2p-cli-macos-arm64" {
			switch kind {
			case "traversal":
				h.Name = "../escaped"
			case "backslash":
				h.Name = `p2p-cli\escaped`
			case "missing":
				continue
			case "symlink":
				h.Typeflag = tar.TypeSymlink
				h.Linkname = "outside"
				payload = nil
			case "hash":
				payload = bytes.Clone(payload)
				payload[0] ^= 1
			case "duplicate":
				if err := tw.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write(payload); err != nil {
					t.Fatal(err)
				}
			}
		}
		h.Size = int64(len(payload))
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
