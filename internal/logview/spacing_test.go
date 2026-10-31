package logview

import (
	"strings"
	"testing"
)

func TestOutputSpacingAcrossChunks(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
	}{
		{"powershell table", "\n\nPath\n----\nC:\\Users\\dmhe\n\n\n", "Path\n----\nC:\\Users\\dmhe\n"},
		{"interior blank lines and indentation", "\n \t\n  first\n \n\n\tsecond\n \n\t", "  first\n \n\n\tsecond\n"},
		{"no final newline", "partial output", "partial output"},
		{"only blank lines", "\n \n\t\n", ""},
		{"unicode", "\n结果\n\n  下一行\n\n", "结果\n\n  下一行\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Every possible split must render exactly like one complete chunk.
			for split := 0; split <= len(tc.input); split++ {
				var output Output
				got := output.Text(tc.input[:split]) + output.Text(tc.input[split:])
				if got != tc.want {
					t.Fatalf("split %d: got %q, want %q", split, got, tc.want)
				}
			}
			var output Output
			var got strings.Builder
			for i := range len(tc.input) {
				got.WriteString(output.Text(tc.input[i : i+1]))
			}
			if got.String() != tc.want {
				t.Fatalf("byte chunks: got %q, want %q", got.String(), tc.want)
			}
		})
	}
}

func TestOutputStreamsTextAndBoundsPendingWhitespace(t *testing.T) {
	var output Output
	if got := output.Text("prompt> "); got != "prompt> " {
		t.Fatalf("partial line delayed: %q", got)
	}
	if got := output.Text("\n\n\n"); got != "\n" {
		t.Fatalf("line ending delayed: %q", got)
	}
	if got := output.Text("response"); got != "\n\nresponse" {
		t.Fatalf("interior blank lines lost: %q", got)
	}
	output.Text(strings.Repeat("\n ", 100000))
	if len(output.pending) > 64*1024 {
		t.Fatalf("unbounded pending output: %d", len(output.pending))
	}
}

func TestSpacingBeforeBlocksAndLines(t *testing.T) {
	for _, tc := range []struct {
		text   string
		chunks []string
		breaks int
		want   string
	}{
		{breaks: 2},
		{text: "no newline", breaks: 2, want: "\n\n"},
		{text: "one newline\n", breaks: 2, want: "\n"},
		{text: "two newlines\n\n", breaks: 2},
		{text: "no newline", breaks: 1, want: "\n"},
		{chunks: []string{"output", "\n", "\n"}, breaks: 2},
	} {
		var spacing Spacing
		spacing.Observe(tc.text)
		for _, chunk := range tc.chunks {
			spacing.Observe(chunk)
		}
		if got := spacing.Before(tc.breaks); got != tc.want {
			t.Errorf("%q %q: got %q, want %q", tc.text, tc.chunks, got, tc.want)
		}
	}
}
