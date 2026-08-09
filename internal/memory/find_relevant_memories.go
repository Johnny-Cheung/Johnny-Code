
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)



type RelevantMemory struct {
	Path    string
	MtimeMs int64
}







type SelectorFn func(ctx context.Context, systemPrompt, userMessage string) (string, error)


const SelectMemoriesSystemPrompt = `You are selecting memories that will be useful to Johnny Code as it processes a user's query. You will be given the user's query and a list of available memory files with their filenames and descriptions.

Return a list of filenames for the memories that will clearly be useful to Johnny Code as it processes the user's query (up to 5). Only include memories that you are certain will be helpful based on their name and description.
- If you are unsure if a memory will be useful in processing the user's query, then do not include it in your list. Be selective and discerning.
- If there are no memories in the list that would clearly be useful, feel free to return an empty list.
- If a list of recently-used tools is provided, do not select memories that are usage reference or API documentation for those tools (Johnny Code is already exercising them). DO still select memories containing warnings, gotchas, or known issues about those tools — active use is exactly when those matter.

Respond with valid JSON only, no markdown, in this exact shape: {"selected_memories": ["filename1.md", "filename2.md"]}`














func FindRelevantMemories(
	ctx context.Context,
	query string,
	userMemDir, projectMemDir string,
	recentTools []string,
	alreadySurfaced map[string]struct{},
	selector SelectorFn,
) ([]RelevantMemory, error) {
	if selector == nil {
		return nil, nil
	}
	var all []MemoryHeader
	if userMemDir != "" {
		userScan, err := ScanMemoryFiles(ctx, userMemDir, "user")
		if err != nil {
			return nil, err
		}
		all = append(all, userScan...)
	}
	if projectMemDir != "" {
		projectScan, err := ScanMemoryFiles(ctx, projectMemDir, "project")
		if err != nil {
			return nil, err
		}
		all = append(all, projectScan...)
	}
	memories := make([]MemoryHeader, 0, len(all))
	for _, m := range all {
		if _, ok := alreadySurfaced[m.FilePath]; ok {
			continue
		}
		memories = append(memories, m)
	}
	if len(memories) == 0 {
		return nil, nil
	}

	selectedFilenames, _ := selectRelevantMemories(ctx, query, memories, recentTools, selector)
	byKey := make(map[string]MemoryHeader, len(memories))
	for _, m := range memories {
		byKey[m.FilePath] = m
		
		if _, exists := byKey[m.Filename]; !exists {
			byKey[m.Filename] = m
		}
	}
	selected := make([]RelevantMemory, 0, len(selectedFilenames))
	for _, fn := range selectedFilenames {
		m, ok := byKey[fn]
		if !ok {
			continue
		}
		selected = append(selected, RelevantMemory{Path: m.FilePath, MtimeMs: m.MtimeMs})
	}
	return selected, nil
}

func selectRelevantMemories(
	ctx context.Context,
	query string,
	memories []MemoryHeader,
	recentTools []string,
	selector SelectorFn,
) ([]string, error) {
	validFilenames := make(map[string]struct{}, len(memories))
	for _, m := range memories {
		validFilenames[m.Filename] = struct{}{}
	}

	manifest := FormatMemoryManifest(memories)

	
	
	
	toolsSection := ""
	if len(recentTools) > 0 {
		toolsSection = "\n\nRecently used tools: " + strings.Join(recentTools, ", ")
	}

	userMessage := fmt.Sprintf("Query: %s\n\nAvailable memories:\n%s%s", query, manifest, toolsSection)

	raw, err := selector(ctx, SelectMemoriesSystemPrompt, userMessage)
	if err != nil {
		return nil, nil
	}
	clean := extractJSONObject(raw)
	if clean == "" {
		return nil, nil
	}
	var parsed struct {
		SelectedMemories []string `json:"selected_memories"`
	}
	if err := json.Unmarshal([]byte(clean), &parsed); err != nil {
		return nil, nil
	}
	out := make([]string, 0, len(parsed.SelectedMemories))
	for _, f := range parsed.SelectedMemories {
		if _, ok := validFilenames[f]; ok {
			out = append(out, f)
		}
	}
	return out, nil
}



func extractJSONObject(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") {
		return trimmed
	}
	start := strings.Index(trimmed, "{")
	if start < 0 {
		return ""
	}
	end := strings.LastIndex(trimmed, "}")
	if end < start {
		return ""
	}
	return trimmed[start : end+1]
}

