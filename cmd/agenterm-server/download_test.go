package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestReleaseDownloads(t *testing.T) {
	assets := fstest.MapFS{
		"agenterm-cli-linux-amd64.tar.gz": {Data: []byte("archive")},
		"SHA256SUMS":                      {Data: []byte("manifest")},
		"README.txt":                      {Data: []byte("private build note")},
	}
	h := downloadHandler("v1.2.3", assets)
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/downloads/v1.2.3/install.sh", 200},
		{"/downloads/v1.2.3/install.ps1", 200},
		{"/downloads/v1.2.3/SHA256SUMS", 200},
		{"/downloads/v1.2.3/agenterm-cli-linux-amd64.tar.gz", 200},
		{"/downloads/v1.2.3/agenterm-cli-windows-arm64.zip", 404},
		{"/downloads/v1.2.3/agenterm-cli-linux-amd64", 404},
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
			if strings.Contains(w.Body.String(), "@RELEASE_TAG@") || strings.Contains(w.Body.String(), "github.com") || !strings.Contains(w.Body.String(), "v1.2.3") {
				t.Fatalf("script not self-contained: %s", tc.path)
			}
		}
	}
	r := httptest.NewRequest("GET", "/downloads/v1.2.3/agenterm-cli-linux-amd64.tar.gz", nil)
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
