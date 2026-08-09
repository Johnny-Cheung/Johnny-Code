package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileDoesNotLoadUnboundedOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("0123456789abcdef\n", 20000)), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadFileTool{FileStateCache: NewFileStateCache()}
	result := tool.Execute(context.Background(), map[string]any{"file_path": path, "limit": 20000})
	if result.IsError {
		t.Fatalf("ReadFile returned error: %s", result.Output)
	}
	if len(result.Output) > MaxOutputChars {
		t.Fatalf("ReadFile output length = %d, want <= %d", len(result.Output), MaxOutputChars)
	}
	if !strings.Contains(result.Output, "ReadFile output truncated") {
		t.Fatalf("missing safety truncation marker")
	}
	if ok, _ := tool.FileStateCache.Check(path); !ok {
		t.Fatal("ReadFile did not record the file as read")
	}
}

func TestReadFileOffsetAndLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "small.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadFileTool{}
	result := tool.Execute(context.Background(), map[string]any{
		"file_path": path,
		"offset":    1,
		"limit":     1,
	})
	if result.Output != "2\ttwo" {
		t.Fatalf("ReadFile output = %q, want %q", result.Output, "2\\ttwo")
	}
}
