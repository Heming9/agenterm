package client

import (
	"fmt"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"
	"io"
	"syscall"
)

func newOutputWriter(w io.Writer) io.WriteCloser {
	kernel := syscall.NewLazyDLL("kernel32.dll")
	codepage, _, _ := kernel.NewProc("GetConsoleOutputCP").Call()
	if codepage == 0 {
		codepage, _, _ = kernel.NewProc("GetOEMCP").Call()
	}
	name := fmt.Sprintf("windows-%d", codepage)
	switch codepage {
	case 936:
		name = "gbk"
	case 950:
		name = "big5"
	case 932:
		name = "shift_jis"
	case 949:
		name = "euc-kr"
	}
	enc, err := htmlindex.Get(name)
	if codepage == 65001 {
		enc = unicode.UTF8
		err = nil
	}
	if err != nil {
		enc = unicode.UTF8
	}
	return &windowsOutput{dest: w, legacy: enc}
}
