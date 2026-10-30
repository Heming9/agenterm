package client

import (
	"bytes"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
	"io"
	"unicode/utf8"
)

// Windows programs can write either UTF-8 or the system ANSI code page to pipes.
// Select a decoder on the first non-ASCII output and keep it for the process.
type windowsOutput struct {
	dest     io.Writer
	selected io.WriteCloser
	pending  []byte
	legacy   encoding.Encoding
}

func (w *windowsOutput) Write(p []byte) (int, error) {
	n := len(p)
	if w.selected != nil {
		_, err := w.selected.Write(p)
		return n, err
	}
	w.pending = append(w.pending, p...)
	idx := bytes.IndexFunc(w.pending, func(r rune) bool { return r >= utf8.RuneSelf })
	if idx < 0 {
		_, err := w.dest.Write(w.pending)
		w.pending = nil
		return n, err
	}
	if idx > 0 {
		if _, err := w.dest.Write(w.pending[:idx]); err != nil {
			return 0, err
		}
		w.pending = w.pending[idx:]
	}
	// An incomplete UTF-8 rune may be split between reads; wait for more data.
	for i := 0; i < len(w.pending); {
		if w.pending[i] < utf8.RuneSelf {
			i++
			continue
		}
		if !utf8.FullRune(w.pending[i:]) {
			return n, nil
		}
		_, size := utf8.DecodeRune(w.pending[i:])
		if size == 1 {
			w.selected = transform.NewWriter(w.dest, w.legacy.NewDecoder())
			break
		}
		i += size
	}
	if w.selected == nil {
		w.selected = transform.NewWriter(w.dest, unicode.UTF8.NewDecoder())
	}
	_, err := w.selected.Write(w.pending)
	w.pending = nil
	return n, err
}
func (w *windowsOutput) Close() error {
	if w.selected == nil && len(w.pending) > 0 {
		w.selected = transform.NewWriter(w.dest, w.legacy.NewDecoder())
		if _, err := w.selected.Write(w.pending); err != nil {
			return err
		}
	}
	if w.selected != nil {
		return w.selected.Close()
	}
	return nil
}
