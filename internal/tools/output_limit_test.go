package tools

import (
	"strings"
	"testing"
)

func TestTruncateOutputKeepsHeadAndTail(t *testing.T) {
	input := "BEGIN-" + strings.Repeat("x", 200) + "-END"
	got := TruncateOutput(input, 80)
	if len(got) > 80 {
		t.Fatalf("truncated output length = %d, want <= 80", len(got))
	}
	if !strings.Contains(got, "BEGIN-") || !strings.Contains(got, "-END") {
		t.Fatalf("truncated output lost head or tail: %q", got)
	}
	if !strings.Contains(got, "tool output truncated") {
		t.Fatalf("missing truncation marker: %q", got)
	}
}

func TestMaxOutputCharsForContext(t *testing.T) {
	if got := MaxOutputCharsForContext(128000); got != MaxOutputChars {
		t.Fatalf("ordinary context cap = %d, want %d", got, MaxOutputChars)
	}
	if got := MaxOutputCharsForContext(800000); got != MaxLargeContextOutputChars {
		t.Fatalf("large context cap = %d, want %d", got, MaxLargeContextOutputChars)
	}
	if got := MaxOutputCharsForContext(16000); got != 16000 {
		t.Fatalf("small context cap = %d, want 16000", got)
	}
}
