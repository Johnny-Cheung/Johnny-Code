package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type GlobTool struct{}

func (t *GlobTool) Name() string { return "Glob" }

func (t *GlobTool) Description() string { return GlobDescription }

func (t *GlobTool) Category() ToolCategory { return CategoryRead }

func (t *GlobTool) Schema() map[string]any {
	return map[string]any{
		"name":        t.Name(),
		"description": t.Description(),
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Glob pattern to match (e.g. '**/*.py')"},
				"path":    map[string]any{"type": "string", "description": "Base directory to search from", "default": "."},
			},
			"required": []string{"pattern"},
		},
	}
}

func (t *GlobTool) Execute(ctx context.Context, args map[string]any) ToolResult {
	pattern, _ := args["pattern"].(string)
	basePath, _ := args["path"].(string)
	if basePath == "" {
		basePath = "."
	}
	if pattern == "" {
		return ToolResult{Output: "Error: pattern is required", IsError: true}
	}

	info, err := os.Stat(basePath)
	if os.IsNotExist(err) {
		return ToolResult{Output: fmt.Sprintf("Error: path not found: %s", basePath), IsError: true}
	}
	if err != nil || !info.IsDir() {
		return ToolResult{Output: fmt.Sprintf("Error: path not found: %s", basePath), IsError: true}
	}

	var matches []string
	if strings.Contains(filepath.ToSlash(pattern), "**") {
		matches, err = recursiveGlob(ctx, basePath, filepath.ToSlash(pattern))
	} else {
		matches, err = flatGlob(basePath, pattern)
	}
	if err != nil {
		return ToolResult{Output: fmt.Sprintf("Error: %s", err), IsError: true}
	}

	sort.Strings(matches)
	if len(matches) == 0 {
		return ToolResult{Output: "No files matched the pattern."}
	}
	return ToolResult{Output: formatGlobOutput(matches)}
}

func formatGlobOutput(matches []string) string {
	joined := strings.Join(matches, "\n")
	if len(joined) > MaxOutputChars {
		joined += fmt.Sprintf("\n[Glob output truncated: %d matching paths; refine the pattern or query a subdirectory]\n", len(matches))
	}
	return TruncateOutput(joined, MaxOutputChars)
}

func flatGlob(basePath, pattern string) ([]string, error) {
	pattern = normalizeGlobPattern(pattern)
	joined := filepath.Join(basePath, filepath.FromSlash(pattern))
	paths, err := filepath.Glob(joined)
	if err != nil {
		return nil, err
	}
	matches := make([]string, 0, len(paths))
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil || (info.IsDir() && SkipDirs[info.Name()]) {
			continue
		}
		rel, relErr := filepath.Rel(basePath, path)
		if relErr != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			rel += "/"
		}
		matches = append(matches, rel)
	}
	return matches, nil
}

func recursiveGlob(ctx context.Context, basePath, pattern string) ([]string, error) {
	var matches []string
	err := filepath.Walk(basePath, func(path string, info os.FileInfo, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			if path != basePath && SkipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(basePath, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if matchGlobPath(pattern, rel) {
			matches = append(matches, rel)
		}
		return nil
	})
	return matches, err
}

func matchGlobPath(pattern, path string) bool {
	patterns := splitGlobPath(normalizeGlobPattern(pattern))
	paths := splitGlobPath(path)
	type key struct{ pi, si int }
	memo := make(map[key]bool)
	done := make(map[key]bool)
	var match func(int, int) bool
	match = func(pi, si int) bool {
		k := key{pi, si}
		if done[k] {
			return memo[k]
		}
		done[k] = true
		if pi == len(patterns) {
			memo[k] = si == len(paths)
			return memo[k]
		}
		if patterns[pi] == "**" {
			memo[k] = match(pi+1, si) || (si < len(paths) && match(pi, si+1))
			return memo[k]
		}
		if si < len(paths) {
			segmentMatch, _ := filepath.Match(patterns[pi], paths[si])
			memo[k] = segmentMatch && match(pi+1, si+1)
		}
		return memo[k]
	}
	return match(0, 0)
}

func normalizeGlobPattern(pattern string) string {
	// Users commonly write [!.]* to mean "anything except a dot-file".
	// Go's filepath.Match uses ^ for class negation, so translate that shell
	// spelling before matching.
	return strings.ReplaceAll(pattern, "[!.]", "[^.]")
}

func splitGlobPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}
