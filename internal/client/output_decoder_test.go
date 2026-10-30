package client

import (
	"bytes"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestWindowsOutputEncoding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks [][]byte
		want   string
	}{
		{"gbk", [][]byte{[]byte("Microsoft ["), {0xb0}, {0xe6, 0xb1, 0xbe}, []byte("]")}, "Microsoft [版本]"},
		{"utf8", [][]byte{[]byte("hello "), {0xe4, 0xb8}, {0xad, 0xe6}, {0x96, 0x87}}, "hello 中文"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dst bytes.Buffer
			w := &windowsOutput{dest: &dst, legacy: simplifiedchinese.GBK}
			for _, chunk := range tc.chunks {
				if _, err := w.Write(chunk); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if dst.String() != tc.want {
				t.Fatalf("got %q want %q", dst.String(), tc.want)
			}
		})
	}
}
