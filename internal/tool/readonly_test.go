package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

func readOnlyTest(t *testing.T) (*ReadOnly, Runtime) {
	t.Helper()
	runtime := testRuntime(t)
	registry, err := NewDefaultRegistry(runtime)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := NewReadOnly(registry, runtime)
	if err != nil {
		t.Fatal(err)
	}
	return readOnly, runtime
}

func executeReadOnly(t *testing.T, readOnly *ReadOnly, name string, arguments any) (Result, error) {
	t.Helper()
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return readOnly.Execute(context.Background(), model.ToolCall{Name: name, Arguments: raw})
}

func TestReadOnlyDefinitionsPreserveFourTools(t *testing.T) {
	readOnly, runtime := readOnlyTest(t)
	registry, err := NewDefaultRegistry(runtime)
	if err != nil {
		t.Fatal(err)
	}
	base := registry.Definitions()
	definitions := readOnly.Definitions()
	if len(definitions) != len(base) {
		t.Fatalf("只读模式不应改变工具数量：%d", len(definitions))
	}
	for i := range base {
		if definitions[i].Name != base[i].Name {
			t.Fatalf("工具顺序/名称变化：%d %s != %s", i, definitions[i].Name, base[i].Name)
		}
		if string(definitions[i].InputSchema) != string(base[i].InputSchema) {
			t.Fatalf("工具 %s 的 schema 被修改", base[i].Name)
		}
	}
	for _, name := range []string{"read", "bash", "write", "edit"} {
		found := false
		for _, definition := range definitions {
			if definition.Name == name && strings.Contains(definition.Description, "只读模式") {
				found = true
			}
		}
		if !found {
			t.Fatalf("工具 %s 缺少只读模式说明", name)
		}
	}
}

func TestReadOnlyReadsInsideAndRejectsOutside(t *testing.T) {
	readOnly, runtime := readOnlyTest(t)
	inside := filepath.Join(runtime.WorkDir, "inside.txt")
	if err := os.WriteFile(inside, []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, ".env"), []byte("SECRET=1"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := executeReadOnly(t, readOnly, "read", map[string]any{"path": "inside.txt"})
	if err != nil || !strings.Contains(result.Content, "inside") {
		t.Fatalf("工作目录内 read 失败：%q %v", result.Content, err)
	}
	result, err = executeReadOnly(t, readOnly, "read", map[string]any{"path": ".env"})
	if err != nil || !strings.Contains(result.Content, "SECRET=1") {
		t.Fatalf("按用户选择应允许读取 .env：%q %v", result.Content, err)
	}
	result, err = executeReadOnly(t, readOnly, "read", map[string]any{"path": inside})
	if err != nil || !strings.Contains(result.Content, "inside") {
		t.Fatalf("工作目录内绝对路径 read 失败：%q %v", result.Content, err)
	}

	outsideBase := t.TempDir()
	outside := filepath.Join(outsideBase, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = executeReadOnly(t, readOnly, "read", map[string]any{"path": outside})
	requireToolKind(t, err, KindPermissionDenied)
	_, err = executeReadOnly(t, readOnly, "read", map[string]any{"path": "../outside.txt"})
	requireToolKind(t, err, KindPermissionDenied)
}

func TestReadOnlyReadMissingKeepsOriginalError(t *testing.T) {
	readOnly, _ := readOnlyTest(t)
	_, err := executeReadOnly(t, readOnly, "read", map[string]any{"path": "missing.txt"})
	requireToolKind(t, err, KindExecution)
	if !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("缺失文件的错误信息异常：%v", err)
	}
}

func TestReadOnlyRejectsWritesWithoutSideEffects(t *testing.T) {
	readOnly, runtime := readOnlyTest(t)
	target := filepath.Join(runtime.WorkDir, "new.txt")
	result, err := executeReadOnly(t, readOnly, "write", map[string]any{"path": "new.txt", "content": "new"})
	requireToolKind(t, err, KindPermissionDenied)
	if !result.IsError || !strings.Contains(result.Content, "只读模式") {
		t.Fatalf("write 拒绝结果异常：%#v", result)
	}
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("只读模式 write 产生了文件：%v", statErr)
	}

	original := filepath.Join(runtime.WorkDir, "edit.txt")
	if err := os.WriteFile(original, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = executeReadOnly(t, readOnly, "edit", map[string]any{"path": "edit.txt", "old_string": "old", "new_string": "new"})
	requireToolKind(t, err, KindPermissionDenied)
	if !result.IsError {
		t.Fatal("edit 拒绝结果应标记失败")
	}
	content, readErr := os.ReadFile(original)
	if readErr != nil || string(content) != "old" {
		t.Fatalf("只读模式 edit 修改了文件：%q %v", content, readErr)
	}
}

func TestReadOnlyBashUsesWhitelist(t *testing.T) {
	readOnly, runtime := readOnlyTest(t)
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "file.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := executeReadOnly(t, readOnly, "bash", map[string]any{"command": "ls -a"})
	if err != nil || !strings.Contains(result.Content, "file.txt") {
		t.Fatalf("只读 bash 查询失败：%q %v", result.Content, err)
	}
	result, err = executeReadOnly(t, readOnly, "bash", map[string]any{"command": "grep -n hello file.txt"})
	if err != nil || !strings.Contains(result.Content, "1:hello") {
		t.Fatalf("只读 bash grep 失败：%q %v", result.Content, err)
	}
	_, err = executeReadOnly(t, readOnly, "bash", map[string]any{"command": "cat file.txt"})
	requireToolKind(t, err, KindPermissionDenied)
	_, err = executeReadOnly(t, readOnly, "bash", map[string]any{"command": "find . -delete"})
	requireToolKind(t, err, KindPermissionDenied)
	_, err = executeReadOnly(t, readOnly, "bash", map[string]any{"command": "ls | head"})
	requireToolKind(t, err, KindPermissionDenied)
}

func TestReadOnlyValidationAndUnknownTools(t *testing.T) {
	readOnly, _ := readOnlyTest(t)
	_, err := executeReadOnly(t, readOnly, "read", map[string]any{"path": "  "})
	requireToolKind(t, err, KindInvalidArguments)
	_, err = executeReadOnly(t, readOnly, "bash", map[string]any{"command": ""})
	requireToolKind(t, err, KindInvalidArguments)
	_, err = executeReadOnly(t, readOnly, "write", map[string]any{"path": "x"})
	requireToolKind(t, err, KindPermissionDenied)
	_, err = executeReadOnly(t, readOnly, "unknown", map[string]any{})
	requireToolKind(t, err, KindInvalidArguments)
}

func TestReadOnlyFutureToolIsDenied(t *testing.T) {
	runtime := testRuntime(t)
	future := &fakeReadOnlyTool{name: "future"}
	registry, err := NewRegistry(NewRead(runtime), NewBash(runtime), NewEdit(runtime), NewWrite(runtime), future)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := NewReadOnly(registry, runtime)
	if err != nil {
		t.Fatal(err)
	}
	_, err = executeReadOnly(t, readOnly, "future", map[string]any{})
	requireToolKind(t, err, KindPermissionDenied)
	if future.calls != 0 {
		t.Fatal("未授权的新工具不应被执行")
	}
}

type fakeReadOnlyTool struct {
	name  string
	calls int
}

func (f *fakeReadOnlyTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{Name: f.name, Description: "测试工具", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (f *fakeReadOnlyTool) Execute(context.Context, json.RawMessage) (Result, error) {
	f.calls++
	return Result{Content: "future"}, nil
}

func TestReadOnlyCanceledContext(t *testing.T) {
	readOnly, _ := readOnlyTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := readOnly.Execute(ctx, model.ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"x"}`)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消上下文应直接返回取消错误：%v", err)
	}
}

func TestReadOnlyDefinitionsAreIndependentCopies(t *testing.T) {
	readOnly, _ := readOnlyTest(t)
	first := readOnly.Definitions()
	first[0].Description = "mutated"
	second := readOnly.Definitions()
	if second[0].Description == "mutated" {
		t.Fatal("Definitions 返回了可被外部修改的共享切片")
	}
}
