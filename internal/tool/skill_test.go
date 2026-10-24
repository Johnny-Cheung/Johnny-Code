package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/skill"
)

func testSkillStore(t *testing.T, workDir string) *skill.Store {
	t.Helper()
	dir := filepath.Join(workDir, ".johnny", "skills", "go-test")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: go-test\ndescription: 运行 Go 测试时使用。\n---\n先运行 $ARGUMENTS。"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return skill.DiscoverWithHome(workDir, t.TempDir())
}

func TestDefaultRegistryWithSkill(t *testing.T) {
	runtime := testRuntime(t)
	store := testSkillStore(t, runtime.WorkDir)
	registry, err := NewDefaultRegistryWithSkill(runtime, store)
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 5 {
		t.Fatalf("工具数量错误：%d", len(definitions))
	}
	found := false
	for _, definition := range definitions {
		if definition.Name == "skill" {
			found = true
			if !strings.Contains(definition.Description, "不授予权限") {
				t.Fatalf("skill 描述缺少权限边界：%s", definition.Description)
			}
		}
	}
	if !found {
		t.Fatal("缺少 skill 工具")
	}

	args, _ := json.Marshal(map[string]string{"name": "go-test", "arguments": "go test ./..."})
	result, err := registry.Execute(context.Background(), model.ToolCall{Name: "skill", Arguments: args})
	if err != nil {
		t.Fatalf("执行 skill 失败：%v", err)
	}
	for _, want := range []string{"Skill：go-test", "来源：项目级", "先运行 go test ./...。", "不能授予权限"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("skill 结果缺少 %q：%s", want, result.Content)
		}
	}
}

func TestSkillToolErrors(t *testing.T) {
	runtime := testRuntime(t)
	store := testSkillStore(t, runtime.WorkDir)
	registry, err := NewDefaultRegistryWithSkill(runtime, store)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"name": "missing"})
	_, err = registry.Execute(context.Background(), model.ToolCall{Name: "skill", Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "未知 Skill") || !strings.Contains(err.Error(), "go-test") {
		t.Fatalf("未知 Skill 应返回可用名称：%v", err)
	}
	_, err = registry.Execute(context.Background(), model.ToolCall{Name: "skill", Arguments: json.RawMessage(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "缺少 name") {
		t.Fatalf("缺少 name 应报错：%v", err)
	}

	empty := skill.DiscoverWithHome(t.TempDir(), t.TempDir())
	registry, err = NewDefaultRegistryWithSkill(runtime, empty)
	if err != nil {
		t.Fatal(err)
	}
	args, _ = json.Marshal(map[string]string{"name": "missing"})
	_, err = registry.Execute(context.Background(), model.ToolCall{Name: "skill", Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "没有可用的 Skill") {
		t.Fatalf("没有 Skill 时应返回明确错误：%v", err)
	}
}

func TestSkillAutoAllowedInPermissionModes(t *testing.T) {
	runtime := testRuntime(t)
	store := testSkillStore(t, runtime.WorkDir)
	registry, err := NewDefaultRegistryWithSkill(runtime, store)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"name": "go-test"})

	rules, err := NewProjectRules(runtime.WorkDir, runtime.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	gate := NewApprovalGate()
	approver := &fakeApprover{}
	gate.Set(func(ctx context.Context, request ApprovalRequest) (ApprovalDecision, error) {
		approver.mu.Lock()
		defer approver.mu.Unlock()
		approver.requests = append(approver.requests, request)
		return ApprovalDecision{Allow: true}, nil
	})
	defaultMode, err := NewDefault(registry, runtime, rules, gate)
	if err != nil {
		t.Fatal(err)
	}
	result, err := defaultMode.Execute(context.Background(), model.ToolCall{Name: "skill", Arguments: args})
	if err != nil || !strings.Contains(result.Content, "go-test") {
		t.Fatalf("默认模式应直接加载 Skill：%v %s", err, result.Content)
	}
	if requests := approver.snapshot(); len(requests) != 0 {
		t.Fatalf("加载 Skill 不应触发审批：%+v", requests)
	}

	readOnly, err := NewReadOnly(registry, runtime)
	if err != nil {
		t.Fatal(err)
	}
	result, err = readOnly.Execute(context.Background(), model.ToolCall{Name: "skill", Arguments: args})
	if err != nil || !strings.Contains(result.Content, "go-test") {
		t.Fatalf("只读模式应允许加载 Skill：%v %s", err, result.Content)
	}
}
