package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

type ReadFileTool struct {
	FileStateCache *FileStateCache
}

func (t *ReadFileTool) Name() string        { return "ReadFile" }
func (t *ReadFileTool) Description() string { return ReadFileDescription }

func (t *ReadFileTool) Category() ToolCategory { return CategoryRead }

func (t *ReadFileTool) Schema() map[string]any {
	return map[string]any{
		"name":        t.Name(),
		"description": t.Description(),
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string", "description": "Absolute or relative path to the file to read"},
				"offset":    map[string]any{"type": "integer", "description": "Line offset to start reading from (0-based)", "default": 0},
				"limit":     map[string]any{"type": "integer", "description": "Maximum number of lines to read", "default": 2000},
			},
			"required": []string{"file_path"},
		},
	}
}

func (t *ReadFileTool) Execute(ctx context.Context, args map[string]any) ToolResult {
	filePath, _ := args["file_path"].(string)
	if filePath == "" {
		return ToolResult{Output: "Error: file_path is required", IsError: true}
	}

	offset := intArg(args, "offset", 0)
	limit := intArg(args, "limit", 2000)
	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}

	info, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		return ToolResult{Output: fmt.Sprintf("Error: file not found: %s", filePath), IsError: true}
	}
	if err != nil {
		return ToolResult{Output: fmt.Sprintf("Error: %s", err), IsError: true}
	}
	if info.IsDir() {
		return ToolResult{Output: fmt.Sprintf("Error: not a file: %s", filePath), IsError: true}
	}

	f, err := os.Open(filePath)
	if err != nil {
		return ToolResult{Output: fmt.Sprintf("Error reading file: %s", err), IsError: true}
	}
	defer f.Close()

	// Stream only the requested line range and stop once the hard result cap is
	// reached. This avoids os.ReadFile allocating an entire attacker-controlled
	// file before the Agent layer gets a chance to enforce its own limit.
	reader := bufio.NewReader(f)
	var sb strings.Builder
	lineNo := 0
	truncated := false
	readErr := error(nil)
	for lineNo < offset+limit {
		if err := ctx.Err(); err != nil {
			return ToolResult{Output: fmt.Sprintf("Error reading file: %s", err), IsError: true}
		}
		selected := lineNo >= offset
		gotLine := false
		lineComplete := false
		for !lineComplete {
			frag, isPrefix, err := reader.ReadLine()
			if err != nil && err != io.EOF {
				readErr = err
				break
			}
			if err == io.EOF && len(frag) == 0 && !gotLine {
				lineComplete = true
				break
			}
			if !gotLine {
				gotLine = true
				if selected {
					prefix := fmt.Sprintf("%d\t", lineNo+1)
					if !appendBounded(&sb, prefix, MaxOutputChars) {
						truncated = true
						break
					}
				}
			}
			if selected && len(frag) > 0 && !appendBounded(&sb, string(frag), MaxOutputChars) {
				truncated = true
				break
			}
			if err == io.EOF {
				lineComplete = true
				break
			}
			if !isPrefix {
				lineComplete = true
			}
		}
		if readErr != nil || truncated {
			break
		}
		if !gotLine {
			break
		}
		if selected && lineNo < offset+limit-1 {
			if !appendBounded(&sb, "\n", MaxOutputChars) {
				truncated = true
				break
			}
		}
		lineNo++
	}
	if readErr != nil {
		return ToolResult{Output: fmt.Sprintf("Error reading file: %s", readErr), IsError: true}
	}

	if t.FileStateCache != nil {
		// Edit/Write only need the fact that this path was read and its mtime;
		// retaining the complete file body here would defeat the streaming guard.
		t.FileStateCache.Record(filePath, "", info.ModTime().UnixMilli())
	}

	output := sb.String()
	if truncated {
		output = TruncateOutput(output+"\n[ReadFile output truncated at the safety limit]", MaxOutputChars)
	}
	return ToolResult{Output: output}
}

func appendBounded(sb *strings.Builder, value string, limit int) bool {
	remaining := limit - sb.Len()
	if remaining <= 0 {
		return false
	}
	if len(value) > remaining {
		n := safeCut(value, remaining)
		if n > 0 {
			sb.WriteString(value[:n])
		}
		return false
	}
	sb.WriteString(value)
	return true
}

func intArg(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return def
}
