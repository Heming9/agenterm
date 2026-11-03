package service

import (
	"agenterm/internal/installer"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func installerFixture(t *testing.T, client []byte) map[string][]byte {
	t.Helper()
	source := make(fstest.MapFS)
	for _, platform := range []string{"macos", "linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			name := fmt.Sprintf("agenterm-client-cli-%s-%s", platform, arch)
			if platform == "windows" {
				name += ".exe"
			}
			source[name] = &fstest.MapFile{Data: client}
		}
	}
	assets, err := installer.BuildDownloads(source)
	if err != nil {
		t.Fatal(err)
	}
	for name, file := range source {
		assets[name] = file.Data
	}
	return assets
}

func TestGeneratedReleaseDownloads(t *testing.T) {
	assets := installerFixture(t, []byte(strings.Repeat("client output\n", 100)))
	source := make(fstest.MapFS)
	for name, data := range assets {
		source[name] = &fstest.MapFile{Data: data}
	}
	h := downloadHandler("v1.2.3", source)
	for name, data := range assets {
		t.Run(name, func(t *testing.T) {
			for _, method := range []string{"GET", "HEAD"} {
				r := httptest.NewRequest(method, "/downloads/v1.2.3/"+name, nil)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 200 || w.Header().Get("Content-Length") != fmt.Sprint(len(data)) {
					t.Fatalf("%s: status %d, headers %v", method, w.Code, w.Header())
				}
				if method == "GET" && !bytes.Equal(w.Body.Bytes(), data) || method == "HEAD" && w.Body.Len() != 0 {
					t.Fatalf("incorrect %s body", method)
				}
			}
			r := httptest.NewRequest("GET", "/downloads/v1.2.3/"+name, nil)
			r.Header.Set("Range", "bytes=1-4")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusPartialContent || !bytes.Equal(w.Body.Bytes(), data[1:5]) {
				t.Fatalf("Range: status %d, body %q", w.Code, w.Body.Bytes())
			}
		})
	}
}

func TestReleaseDownloads(t *testing.T) {
	assets := fstest.MapFS{
		"agenterm-client-cli-linux-amd64.tar.xz": {Data: []byte("archive")},
		"SHA256SUMS":                             {Data: []byte("manifest")},
		"README.txt":                             {Data: []byte("private build note")},
	}
	h := downloadHandler("v1.2.3", assets)
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/downloads/v1.2.3/install.sh", 200},
		{"/downloads/v1.2.3/install.ps1", 200},
		{"/downloads/v1.2.3/SHA256SUMS", 200},
		{"/downloads/v1.2.3/agenterm-client-cli-linux-amd64.tar.xz", 200},
		{"/downloads/v1.2.3/agenterm-client-cli-windows-arm64.tar.xz", 404},
		{"/downloads/v1.2.3/agenterm-client-cli-linux-amd64", 404},
		{"/downloads/v1.2.3/README.txt", 404},
		{"/downloads/v1.2.3/", 404},
		{"/downloads/v1.2.3/../SHA256SUMS", 404},
		{"/downloads/v0.0.1/install.sh", 404},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("%s: status=%d", tc.path, w.Code)
		}
		if tc.status == 200 && strings.Contains(tc.path, "install.") {
			// Project attribution may link to GitHub; downloads must stay local.
			if strings.Contains(w.Body.String(), "@RELEASE_TAG@") || strings.Contains(w.Body.String(), "github.com/Heming9/agenterm/releases/download/") || strings.Contains(w.Body.String(), "raw.githubusercontent.com") || !strings.Contains(w.Body.String(), "v1.2.3") {
				t.Fatalf("script not self-contained: %s", tc.path)
			}
		}
	}
	r := httptest.NewRequest("GET", "/downloads/v1.2.3/agenterm-client-cli-linux-amd64.tar.xz", nil)
	r.Header.Set("Range", "bytes=0-2")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusPartialContent || w.Body.String() != "arc" {
		t.Fatalf("range download: %d %q", w.Code, w.Body.String())
	}
	for _, method := range []string{"HEAD", "POST"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/downloads/v1.2.3/SHA256SUMS", nil))
		if method == "HEAD" && (w.Code != 200 || w.Body.Len() != 0) || method == "POST" && w.Code != 405 {
			t.Fatalf("method %s: %d %q", method, w.Code, w.Body.String())
		}
	}
	w = httptest.NewRecorder()
	downloadHandler("", assets).ServeHTTP(w, httptest.NewRequest("GET", "/downloads//install.sh", nil))
	if w.Code != 404 {
		t.Fatalf("development server exposed installer: %d", w.Code)
	}
}
