package client

import (
	"strings"
	"testing"
)

func TestPromptFramesSplitAndInvalidFrames(t *testing.T) {
	const prefix = "\x1erandom:"
	input := "你好" + prefix + "7\x1f" + "output" + prefix + "invalid\x1f" + prefix[:4]
	for size := 1; size <= len(input); size++ {
		var output strings.Builder
		var codes []int
		f := &promptFrames{prefix: []byte(prefix), output: func(p []byte) error { output.Write(p); return nil }, complete: func(code int) { codes = append(codes, code) }}
		for i := 0; i < len(input); i += size {
			if _, err := f.Write([]byte(input[i:min(i+size, len(input))])); err != nil {
				t.Fatal(err)
			}
		}
		f.flush()
		if output.String() != "你好output"+prefix+"invalid\x1f"+prefix[:4] || len(codes) != 1 || codes[0] != 7 {
			t.Fatalf("chunk %d: %q %v", size, output.String(), codes)
		}
	}
}
