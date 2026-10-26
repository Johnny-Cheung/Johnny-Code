package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/memory"
)

func testMemoryController(t *testing.T, writable bool) *memoryController {
	t.Helper()
	home := t.TempDir()
	work := t.TempDir()
	store, err := memory.NewStoreAt(home, work)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), memory.Item{
		Name: "go-test", Description: "提交前运行 go test", Type: memory.TypeProject,
		Scope: memory.ScopeProject, Confidence: memory.ConfidenceHigh, Body: "正文",
	}); err != nil {
		t.Fatal(err)
	}
	return &memoryController{store: store, writable: writable}
}

func TestMemoryCommandShowsIndexes(t *testing.T) {
	controller := testMemoryController(t, true)
	result, handled := handleMemoryCommand("/memory", controller)
	if !handled || !strings.Contains(result.Output, "用户记忆索引") || !strings.Contains(result.Output, "项目记忆索引") || !strings.Contains(result.Output, "`go-test`") {
		t.Fatalf("/memory 输出错误：%+v", result)
	}
	if strings.TrimSpace(result.Prompt) != "" {
		t.Fatal("/memory 不应发起模型任务")
	}
}

func TestRememberCommandBuildsProjectAgentsPrompt(t *testing.T) {
	controller := testMemoryController(t, true)
	result, handled := handleMemoryCommand("/remember", controller)
	if !handled || !strings.Contains(result.Prompt, "项目 AGENTS.md") || !strings.Contains(result.Prompt, "不要直接写入") {
		t.Fatalf("/remember 提示错误：%+v", result)
	}
	result, handled = handleMemoryCommand("/remember 提交前运行 go test", controller)
	if !handled || !strings.Contains(result.Prompt, "提交前运行 go test") {
		t.Fatalf("/remember 参数未带入：%+v", result)
	}
}

func TestRememberCommandReadOnly(t *testing.T) {
	controller := testMemoryController(t, false)
	result, handled := handleMemoryCommand("/remember", controller)
	if !handled || !strings.Contains(result.Output, "只读模式") {
		t.Fatalf("只读模式应拒绝 /remember：%+v", result)
	}
	if result.Prompt != "" {
		t.Fatal("只读模式不应产生任务")
	}
}

func TestMemoryCommandDisabled(t *testing.T) {
	result, handled := handleMemoryCommand("/memory", nil)
	if !handled || !strings.Contains(result.Output, "未启用长期记忆") {
		t.Fatalf("未启用时应提示：%+v", result)
	}
}
