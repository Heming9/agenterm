package logview

import "testing"

func TestDecoderChunkBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"utf8", "hello 中🙂\r\nworld", "hello 中🙂\nworld"},
		{"csi", "A\x1b[31m红\x1b[0mB", "A红B"},
		{"osc bell", "A\x1b]0;hidden title\aB", "AB"},
		{"osc terminator", "A\x1b]0;hidden title\x1b\\B", "AB"},
		{"controls", "A\x00\x08\x7f\tB\n", "A\tB\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for size := 1; size <= len(tc.input); size++ {
				var decoder Decoder
				var got string
				for start := 0; start < len(tc.input); start += size {
					got += decoder.Text(tc.input[start:min(start+size, len(tc.input))])
				}
				if got != tc.want {
					t.Fatalf("chunk size %d: got %q, want %q", size, got, tc.want)
				}
			}
		})
	}
}
