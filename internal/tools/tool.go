package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

var SkipDirs = map[string]bool{
	".git": true, ".venv": true, "node_modules": true,
	"__pycache__": true, ".tox": true, ".mypy_cache": true,
}

// MaxOutputChars is the conservative per-tool-result safety cap used for
// providers with ordinary context windows. It is deliberately well above the
// old 10k preview limit so the tool-result spill and compaction layers can see
// a useful amount of the original output first.
const MaxOutputChars = 100000

// MaxLargeContextOutputChars is used for providers configured with a very
// large context window. It is still an absolute cap: a tool must never be
// allowed to hand an unbounded string to the agent loop.
const MaxLargeContextOutputChars = 200000

// MaxOutputCharsForContext returns the absolute per-result cap appropriate for
// a model context window measured in tokens. A small window gets a smaller cap
// so one result cannot consume the whole prompt; large-window models may use
// the larger 200k-character ceiling.
func MaxOutputCharsForContext(contextWindow int) int {
	limit := MaxOutputChars
	if contextWindow >= 400000 {
		limit = MaxLargeContextOutputChars
	}
	if contextWindow > 0 {
		// With roughly three bytes per token, one context-window token's worth
		// of characters is about one third of the available prompt budget.
		byWindow := contextWindow
		if byWindow < limit {
			limit = byWindow
		}
	}
	if limit < 4096 {
		return 4096
	}
	return limit
}

// TruncateOutput applies the hard safety cap while retaining both the start
// and end of the result. The end often contains the actual error or summary,
// so keeping only a prefix is needlessly destructive.
func TruncateOutput(output string, limit int) string {
	if limit <= 0 || len(output) <= limit {
		return output
	}
	marker := fmt.Sprintf("\n[tool output truncated; original %d chars]\n", len(output))
	if len(marker) >= limit {
		return output[:safeCut(output, limit)]
	}
	remaining := limit - len(marker)
	headLen := remaining * 2 / 3
	tailLen := remaining - headLen
	headLen = safeCut(output, headLen)
	tailStart := len(output) - tailLen
	for tailStart < len(output) && !utf8.RuneStart(output[tailStart]) {
		tailStart++
	}
	return output[:headLen] + marker + output[tailStart:]
}

func safeCut(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	if n <= 0 {
		return 0
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

type ToolResult struct {
	Output  string
	IsError bool
}

type ToolCategory string

const (
	CategoryRead    ToolCategory = "read"
	CategoryWrite   ToolCategory = "write"
	CategoryCommand ToolCategory = "command"
)

type Tool interface {
	Name() string
	Description() string
	Category() ToolCategory
	Schema() map[string]any
	Execute(ctx context.Context, args map[string]any) ToolResult
}

type DeferrableTool interface {
	ShouldDefer() bool
}

type SystemTool interface {
	IsSystemTool() bool
}

func IsSystemTool(t Tool) bool {
	st, ok := t.(SystemTool)
	return ok && st.IsSystemTool()
}

type Registry struct {
	tools           map[string]Tool
	discoveredTools map[string]bool
}

// DeferredToolInfo is the lightweight catalog entry shown before a deferred
// tool's full parameter schema is loaded.
type DeferredToolInfo struct {
	Name        string
	Description string
}

func NewRegistry() *Registry {
	return &Registry{
		tools:           make(map[string]Tool),
		discoveredTools: make(map[string]bool),
	}
}

func (r *Registry) MarkDiscovered(name string) {
	r.discoveredTools[name] = true
}

func (r *Registry) IsDiscovered(name string) bool {
	return r.discoveredTools[name]
}

func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

func (r *Registry) sortedToolNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) ListTools() []Tool {
	result := make([]Tool, 0, len(r.tools))
	for _, name := range r.sortedToolNames() {
		result = append(result, r.tools[name])
	}
	return result
}

func isDeferred(t Tool) bool {
	if dt, ok := t.(DeferrableTool); ok {
		return dt.ShouldDefer()
	}
	return false
}

func (r *Registry) GetAllSchemas(protocol string) []map[string]any {
	schemas := make([]map[string]any, 0, len(r.tools))
	for _, name := range r.sortedToolNames() {
		t := r.tools[name]
		if isDeferred(t) && !r.discoveredTools[t.Name()] {
			continue
		}
		base := t.Schema()
		if protocol == "openai" {
			schemas = append(schemas, map[string]any{
				"type":        "function",
				"name":        base["name"],
				"description": base["description"],
				"parameters":  base["input_schema"],
			})
		} else {
			schemas = append(schemas, base)
		}
	}
	return schemas
}

func (r *Registry) GetDeferredToolNames() []string {
	var names []string
	for _, name := range r.sortedToolNames() {
		t := r.tools[name]
		if isDeferred(t) && !r.discoveredTools[t.Name()] {
			names = append(names, t.Name())
		}
	}
	return names
}

// GetDeferredToolInfos returns name and description metadata for deferred
// tools that have not yet been discovered. The full input schema remains
// omitted until ToolSearch marks the tool as discovered.
func (r *Registry) GetDeferredToolInfos() []DeferredToolInfo {
	var infos []DeferredToolInfo
	for _, name := range r.sortedToolNames() {
		t := r.tools[name]
		if isDeferred(t) && !r.discoveredTools[t.Name()] {
			infos = append(infos, DeferredToolInfo{
				Name:        t.Name(),
				Description: t.Description(),
			})
		}
	}
	return infos
}

func (r *Registry) GetDeferredTools() []Tool {
	var result []Tool
	for _, name := range r.sortedToolNames() {
		t := r.tools[name]
		if isDeferred(t) {
			result = append(result, t)
		}
	}
	return result
}

func (r *Registry) SearchDeferred(query string, maxResults int, protocol string) []map[string]any {
	query = strings.ToLower(query)
	var matches []map[string]any
	for _, name := range r.sortedToolNames() {
		t := r.tools[name]
		if !isDeferred(t) {
			continue
		}
		name := strings.ToLower(t.Name())
		desc := strings.ToLower(t.Description())
		if strings.Contains(name, query) || strings.Contains(desc, query) {
			base := t.Schema()
			if protocol == "openai" {
				matches = append(matches, map[string]any{
					"type":        "function",
					"name":        base["name"],
					"description": base["description"],
					"parameters":  base["input_schema"],
				})
			} else {
				matches = append(matches, base)
			}
			if len(matches) >= maxResults {
				break
			}
		}
	}
	return matches
}

func (r *Registry) FindDeferredByNames(names []string, protocol string) []map[string]any {
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[strings.ToLower(n)] = true
	}
	var matches []map[string]any
	for _, name := range r.sortedToolNames() {
		t := r.tools[name]
		if nameSet[strings.ToLower(t.Name())] {
			base := t.Schema()
			if protocol == "openai" {
				matches = append(matches, map[string]any{
					"type":        "function",
					"name":        base["name"],
					"description": base["description"],
					"parameters":  base["input_schema"],
				})
			} else {
				matches = append(matches, base)
			}
		}
	}
	return matches
}

type DefaultTools struct {
	Registry  *Registry
	WriteFile *WriteFileTool
	EditFile  *EditFileTool
}

func CreateDefaultRegistry() *Registry {
	dt := CreateDefaultTools()
	return dt.Registry
}

func CreateDefaultTools() DefaultTools {
	fsc := NewFileStateCache()
	wf := &WriteFileTool{FileStateCache: fsc}
	ef := &EditFileTool{FileStateCache: fsc}
	reg := NewRegistry()
	reg.Register(&ReadFileTool{FileStateCache: fsc})
	reg.Register(wf)
	reg.Register(ef)
	reg.Register(&BashTool{})
	reg.Register(&GlobTool{})
	reg.Register(&GrepTool{})
	return DefaultTools{Registry: reg, WriteFile: wf, EditFile: ef}
}
