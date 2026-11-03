package components

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture() map[string][]byte {
	files := map[string][]byte{"client-cli/SHA256SUMS": []byte("checksums"), "p2p-cli/manifest.json": []byte("{}"), "p2p-cli/p2p-cli-macos-arm64.tar.gz": []byte("native p2p archive")}
	for _, platform := range []string{"macos", "linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			ext := ".tar.gz"
			if platform == "windows" {
				ext = ".zip"
			}
			name := "client-cli/agenterm-client-cli-" + platform + "-" + arch + ext
			files[name] = []byte("archive for " + name)
		}
	}
	return files
}
func packed(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Pack(&out, files); err != nil {
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
		t.Fatal("components.zip is not deterministic")
	}
	base := privateBase(t)
	paths, err := install(base, data)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(paths.Root) != filepath.Join(base, "components") {
		t.Fatal(paths.Root)
	}
	got, err := fs.ReadFile(paths.ClientCLI(), "SHA256SUMS")
	if err != nil || !bytes.Equal(got, files["client-cli/SHA256SUMS"]) {
		t.Fatal(err)
	}
	file := filepath.Join(paths.P2PDirectory(), "p2p-cli-macos-arm64.tar.gz")
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
		t.Fatal("valid cache was rewritten")
	}
	files["p2p-cli/p2p-cli-macos-arm64.tar.gz"] = []byte("new archive")
	updated, err := install(base, packed(t, files))
	if err != nil || updated.Root == paths.Root {
		t.Fatal(err)
	}
	old, err := os.ReadFile(file)
	if err != nil || string(old) != "native p2p archive" {
		t.Fatal("an update changed an old frontend's assets")
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
		t.Fatalf("partial or extra installation: %v %v", entries, err)
	}
}
func TestCachedTamperingAndDirectorySymlinksAreRejected(t *testing.T) {
	data := packed(t, fixture())
	base := privateBase(t)
	paths, err := install(base, data)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(paths.P2PDirectory(), "p2p-cli-macos-arm64.tar.gz"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = install(base, data); err == nil {
		t.Fatal("tampered cache accepted")
	}
	if runtime.GOOS == "windows" {
		return
	}
	other := privateBase(t)
	outside := t.TempDir()
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
	for _, kind := range []string{"traversal", "backslash", "duplicate", "missing", "symlink", "hash", "oversize", "trailing-manifest", "crc", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			bad := rewriteBundle(t, data, kind)
			base := privateBase(t)
			if _, err := install(base, bad); err == nil {
				t.Fatal("invalid bundle accepted")
			}
			entries, _ := os.ReadDir(filepath.Join(base, "components"))
			if len(entries) != 0 {
				t.Fatal("failed extraction left published or temporary files")
			}
		})
	}
}
func rewriteBundle(t *testing.T, data []byte, kind string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if kind == "truncated" {
		return data[:len(data)-5]
	}
	if kind == "crc" {
		bad := bytes.Clone(data)
		for _, entry := range zr.File {
			if strings.HasPrefix(entry.Name, "p2p-cli/p2p-cli-") {
				offset, _ := entry.DataOffset()
				bad[offset] ^= 1
				break
			}
		}
		return bad
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	altered := false
	for _, entry := range zr.File {
		payload, err := fs.ReadFile(zr, entry.Name)
		if err != nil {
			t.Fatal(err)
		}
		header := entry.FileHeader
		if entry.Name == "manifest.json" && kind == "trailing-manifest" {
			payload = append(payload, []byte(" {}")...)
		}
		if entry.Name == "manifest.json" && kind == "oversize" {
			var m manifest
			if err = json.Unmarshal(payload, &m); err != nil {
				t.Fatal(err)
			}
			info := m.Files["p2p-cli/p2p-cli-macos-arm64.tar.gz"]
			info.Bytes = maxFileBytes + 1
			m.Files["p2p-cli/p2p-cli-macos-arm64.tar.gz"] = info
			payload, _ = json.Marshal(m)
		}
		if !altered && strings.HasPrefix(entry.Name, "p2p-cli/p2p-cli-") {
			altered = true
			switch kind {
			case "traversal":
				header.Name = "../escaped"
			case "backslash":
				header.Name = `p2p-cli\escaped`
			case "missing":
				continue
			case "symlink":
				header.SetMode(os.ModeSymlink | 0777)
			case "hash":
				payload[0] ^= 1
			}
			if kind == "duplicate" {
				duplicate := header
				w, _ := zw.CreateHeader(&duplicate)
				_, _ = w.Write(payload)
			}
		}
		w, err := zw.CreateHeader(&header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
