//go:build !windows

package client

import (
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
	"io"
)

// Buffer incomplete UTF-8 runes so JSON output frames cannot corrupt them.
func newOutputWriter(w io.Writer) io.WriteCloser {
	return transform.NewWriter(w, unicode.UTF8.NewDecoder())
}
