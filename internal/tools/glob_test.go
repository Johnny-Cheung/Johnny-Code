package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupGlobTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := []string{
		"main.go",
		"cmd/cli/main.go",
		"internal/agents/agent.go",
		"internal/agents/agent_test.go",
		"docs/readme.md",
	}
	for _, rel := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestGlobDoubleStarPattern(t *testing.T) {

	root := setupGlobTree(t)
	tool := &GlobTool{}
	res := tool.Execute(context.Background(), map[string]any{
		"pattern": "**/*.go",
		"path":    root,
	})
	if res.IsError {
		t.Fatalf("glob errored: %s", res.Output)
	}
	for _, want := range []string{"main.go", "cmd/cli/main.go", "internal/agents/agent.go", "internal/agents/agent_test.go"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("expected %q in output, got:\n%s", want, res.Output)
		}
	}
	if strings.Contains(res.Output, "readme.md") {
		t.Errorf("readme.md should NOT match **/*.go")
	}
}

func TestGlobPlainPatternStillWorks(t *testing.T) {
	root := setupGlobTree(t)
	tool := &GlobTool{}
	res := tool.Execute(context.Background(), map[string]any{
		"pattern": "*.go",
		"path":    root,
	})
	if res.IsError {
		t.Fatalf("glob errored: %s", res.Output)
	}

	if !strings.Contains(res.Output, "main.go") {
		t.Errorf("plain pattern should still match base names, got:\n%s", res.Output)
	}
}

func TestGlobStarOnlyListsTopLevelEntries(t *testing.T) {
	root := setupGlobTree(t)
	tool := &GlobTool{}
	res := tool.Execute(context.Background(), map[string]any{
		"pattern": "*",
		"path":    root,
	})
	if res.IsError {
		t.Fatalf("glob errored: %s", res.Output)
	}
	if !strings.Contains(res.Output, "main.go") || !strings.Contains(res.Output, "cmd/") {
		t.Fatalf("top-level entries missing: %s", res.Output)
	}
	if strings.Contains(res.Output, "cmd/cli/main.go") || strings.Contains(res.Output, "internal/agents/agent.go") {
		t.Fatalf("non-recursive '*' unexpectedly traversed subdirectories: %s", res.Output)
	}
}

func TestGlobBracketNegationExcludesDotEntries(t *testing.T) {
	root := setupGlobTree(t)
	if err := os.Mkdir(filepath.Join(root, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := &GlobTool{}
	res := tool.Execute(context.Background(), map[string]any{
		"pattern": "[!.]*",
		"path":    root,
	})
	if res.IsError {
		t.Fatalf("glob errored: %s", res.Output)
	}
	if strings.Contains(res.Output, ".hidden") {
		t.Fatalf("dot entry unexpectedly matched: %s", res.Output)
	}
}

func TestGlobLargeOutputExplainsTruncation(t *testing.T) {
	matches := make([]string, 12000)
	for i := range matches {
		matches[i] = strings.Repeat("x", 12) + ".go"
	}
	got := formatGlobOutput(matches)
	if len(got) > MaxOutputChars {
		t.Fatalf("formatted output length = %d, want <= %d", len(got), MaxOutputChars)
	}
	if !strings.Contains(got, "Glob output truncated") || !strings.Contains(got, "12000") {
		t.Fatalf("truncation explanation missing: %q", got[len(got)-120:])
	}
}
