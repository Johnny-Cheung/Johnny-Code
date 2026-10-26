package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/memory"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/skill"
)

func testMemoryTool(t *testing.T) (*MemoryTool, *memory.Store) {
	t.Helper()
	home := t.TempDir()
	work := t.TempDir()
	store, err := memory.NewStoreAt(home, work)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.Write(ctx, memory.Item{
		Name: "go-test", Description: "提交前运行 go test", Type: memory.TypeProject,
		Scope: memory.ScopeProject, Confidence: memory.ConfidenceHigh, Body: "提交前运行 `go test ./...`。",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, memory.Item{
		Name: "answer-first", Description: "回答先给结论", Type: memory.TypePreference,
		Scope: memory.ScopeUser, Confidence: memory.ConfidenceMedium, Body: "先给结论，再展开。",
	}); err != nil {
		t.Fatal(err)
	}
	tool, err := NewMemoryTool(store)
	if err != nil {
		t.Fatal(err)
	}
	return tool, store
}

func executeMemory(t *testing.T, tool *MemoryTool, args string) (string, error) {
	t.Helper()
	result, err := tool.Execute(context.Background(), json.RawMessage(args))
	return result.Content, err
}

func TestMemoryToolSearchReadList(t *testing.T) {
	tool, _ := testMemoryTool(t)
	content, err := executeMemory(t, tool, `{"action":"search","query":"go test"}`)
	if err != nil || !strings.Contains(content, "go-test") || !strings.Contains(content, "[project]") {
		t.Fatalf("搜索失败：%q %v", content, err)
	}
	content, err = executeMemory(t, tool, `{"action":"read","scope":"project","name":"go-test"}`)
	if err != nil || !strings.Contains(content, "提交前运行") || !strings.Contains(content, "作用域：project") {
		t.Fatalf("读取失败：%q %v", content, err)
	}
	content, err = executeMemory(t, tool, `{"action":"list"}`)
	if err != nil || !strings.Contains(content, "answer-first") || !strings.Contains(content, "go-test") {
		t.Fatalf("列表失败：%q %v", content, err)
	}
}

func TestMemoryToolReadWithoutScopeRejectsAmbiguity(t *testing.T) {
	tool, store := testMemoryTool(t)
	ctx := context.Background()
	if _, err := store.Write(ctx, memory.Item{
		Name: "same-name", Description: "项目", Type: memory.TypeProject,
		Scope: memory.ScopeProject, Confidence: memory.ConfidenceHigh, Body: "项目正文",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, memory.Item{
		Name: "same-name", Description: "用户", Type: memory.TypePreference,
		Scope: memory.ScopeUser, Confidence: memory.ConfidenceHigh, Body: "用户正文",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := executeMemory(t, tool, `{"action":"read","name":"same-name"}`); err == nil {
		t.Fatal("同名跨作用域应要求指定 scope")
	}
	content, err := executeMemory(t, tool, `{"action":"read","scope":"user","name":"same-name"}`)
	if err != nil || !strings.Contains(content, "用户正文") {
		t.Fatalf("指定 scope 读取失败：%q %v", content, err)
	}
}

func TestMemoryToolRejectsInvalidArguments(t *testing.T) {
	tool, _ := testMemoryTool(t)
	if _, err := executeMemory(t, tool, `{"action":"delete"}`); err == nil {
		t.Fatal("未知 action 应报错")
	}
	if _, err := executeMemory(t, tool, `{"action":"search","query":""}`); err == nil {
		t.Fatal("空搜索应报错")
	}
	if _, err := executeMemory(t, tool, `{"action":"read","scope":"other","name":"x"}`); err == nil {
		t.Fatal("非法 scope 应报错")
	}
}

func TestDefaultRegistryWithMemory(t *testing.T) {
	_, store := testMemoryTool(t)
	skills := skill.Discover(t.TempDir())
	runtime, err := NewRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewDefaultRegistryWithSkillAndMemory(runtime, skills, store)
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 6 {
		t.Fatalf("工具数量错误：%d", len(definitions))
	}
	if !strings.Contains(strings.Join(toolNames(definitions), ","), "memory") {
		t.Fatalf("缺少 memory 工具：%v", toolNames(definitions))
	}
	result, err := registry.Execute(context.Background(), model.ToolCall{
		Name: "memory", Arguments: json.RawMessage(`{"action":"list"}`),
	})
	if err != nil || !strings.Contains(result.Content, "go-test") {
		t.Fatalf("注册表执行 memory 失败：%q %v", result.Content, err)
	}
}

func TestReadOnlyAllowsMemory(t *testing.T) {
	_, store := testMemoryTool(t)
	skills := skill.Discover(t.TempDir())
	runtime, err := NewRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base, err := NewDefaultRegistryWithSkillAndMemory(runtime, skills, store)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := NewReadOnly(base, runtime)
	if err != nil {
		t.Fatal(err)
	}
	result, err := readOnly.Execute(context.Background(), model.ToolCall{
		Name: "memory", Arguments: json.RawMessage(`{"action":"list"}`),
	})
	if err != nil || !strings.Contains(result.Content, "go-test") {
		t.Fatalf("只读模式应允许 memory：%q %v", result.Content, err)
	}
}

func toolNames(definitions []model.ToolDefinition) []string {
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	return names
}
