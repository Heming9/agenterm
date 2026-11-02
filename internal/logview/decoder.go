package logview

import (
	"strings"
	"unicode/utf8"
)

// Decoder strips terminal control sequences while preserving split UTF-8.
// Its zero value is ready to use; keep one decoder per output stream.
type Decoder struct {
	state   byte
	pending string
	Output  Output
}

func (d *Decoder) Text(chunk string) string {
	chunk = d.pending + chunk
	d.pending = ""
	var out strings.Builder
	for len(chunk) > 0 {
		if !utf8.FullRuneInString(chunk) {
			d.pending = chunk
			break
		}
		r, n := utf8.DecodeRuneInString(chunk)
		chunk = chunk[n:]
		switch d.state {
		case 1:
			switch r {
			case '[':
				d.state = 2
			case ']':
				d.state = 3
			default:
				d.state = 0
			}
		case 2:
			if r >= 0x40 && r <= 0x7e {
				d.state = 0
			}
		case 3:
			if r == 7 {
				d.state = 0
			} else if r == 27 {
				d.state = 4
			}
		case 4:
			if r == '\\' {
				d.state = 0
			} else {
				d.state = 3
			}
		default:
			if r == 27 {
				d.state = 1
			} else if r == '\n' || r == '\t' || r >= 32 && r != 127 {
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}
