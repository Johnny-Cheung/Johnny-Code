package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/memory"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/skill"
)

func testRememberTool(t *testing.T) (*RememberTool, *memory.Store, *memory.Rules, string) {
	t.Helper()
	home := t.TempDir()
	work := t.TempDir()
	store, err := memory.NewStoreAt(home, work)
	if err != nil {
		t.Fatal(err)
	}
	rules := memory.NewRules(home)
	tool, err := NewRememberTool(store, rules, "session-test")
	if err != nil {
		t.Fatal(err)
	}
	return tool, store, rules, home
}

func executeRemember(t *testing.T, tool *RememberTool, args string) (Result, error) {
	t.Helper()
	return tool.Execute(context.Background(), json.RawMessage(args))
}

func TestRememberWritesMemory(t *testing.T) {
	tool, store, _, _ := testRememberTool(t)
	result, err := executeRemember(t, tool, `{"target":"project_memory","type":"project","name":"go-test","description":"提交前运行 go test","body":"提交前运行 go test ./...","confidence":"high"}`)
	if err != nil || !strings.Contains(result.Content, "已记录项目记忆") {
		t.Fatalf("写入项目记忆失败：%q %v", result.Content, err)
	}
	item, err := store.Read(memory.ScopeProject, "go-test")
	if err != nil || item.Source != "session-test" || !strings.Contains(item.Body, "go test") {
		t.Fatalf("项目记忆内容错误：%+v %v", item, err)
	}
	result, err = executeRemember(t, tool, `{"target":"user_memory","type":"preference","name":"answer-first","description":"先给结论","body":"先给结论，再展开。","confidence":"medium"}`)
	if err != nil || !strings.Contains(result.Content, "已记录用户记忆") {
		t.Fatalf("写入用户记忆失败：%q %v", result.Content, err)
	}
}

func TestRememberWritesUserAgents(t *testing.T) {
	tool, _, rules, _ := testRememberTool(t)
	result, err := executeRemember(t, tool, `{"target":"user_agents","name":"answer-language-zh","description":"所有回答使用中文","confidence":"high","evidence":"用户：以后所有项目回答都用中文"}`)
	if err != nil || !strings.Contains(result.Content, "已记录用户规则") {
		t.Fatalf("写入用户规则失败：%q %v", result.Content, err)
	}
	data, err := os.ReadFile(rules.Path())
	if err != nil || !strings.Contains(string(data), "`answer-language-zh`") {
		t.Fatalf("用户规则文件错误：%s %v", data, err)
	}
}

func TestRememberRejectsUnsafeUserAgents(t *testing.T) {
	tool, _, _, _ := testRememberTool(t)
	for name, args := range map[string]string{
		"低置信度": `{"target":"user_agents","name":"rule","description":"规则","confidence":"medium","evidence":"用户说的"}`,
		"缺证据":  `{"target":"user_agents","name":"rule","description":"规则","confidence":"high"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := executeRemember(t, tool, args); err == nil {
				t.Fatal("应拒绝")
			}
		})
	}
}

func TestRememberRejectsInvalidArguments(t *testing.T) {
	tool, _, _, _ := testRememberTool(t)
	for name, args := range map[string]string{
		"未知 target": `{"target":"project_agents","name":"x","description":"x","body":"x"}`,
		"缺 body":    `{"target":"project_memory","name":"x","description":"x"}`,
		"密钥":        `{"target":"project_memory","name":"secret","description":"x","body":"JOHNNY_API_KEY=value"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := executeRemember(t, tool, args); err == nil {
				t.Fatal("应拒绝")
			}
		})
	}
}

func TestRegistryWithRemember(t *testing.T) {
	_, store, rules, home := testRememberTool(t)
	skills := skill.Discover(home)
	runtime, err := NewRuntime(home)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewDefaultRegistryWithMemory(runtime, skills, store, rules, "session-test")
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 7 {
		t.Fatalf("工具数量错误：%d", len(definitions))
	}
	names := make(map[string]bool)
	for _, definition := range definitions {
		names[definition.Name] = true
	}
	for _, want := range []string{"memory", "remember"} {
		if !names[want] {
			t.Fatalf("缺少工具 %s：%v", want, names)
		}
	}
}

func TestReadOnlyRejectsRemember(t *testing.T) {
	_, store, rules, home := testRememberTool(t)
	skills := skill.Discover(home)
	runtime, err := NewRuntime(home)
	if err != nil {
		t.Fatal(err)
	}
	base, err := NewDefaultRegistryWithMemory(runtime, skills, store, rules, "session-test")
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := NewReadOnly(base, runtime)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readOnly.Execute(context.Background(), model.ToolCall{
		Name: "remember", Arguments: json.RawMessage(`{"target":"project_memory","name":"x","description":"x","body":"x"}`),
	})
	if err == nil {
		t.Fatal("只读模式应拒绝 remember")
	}
}

func TestDefaultModeRemembersAfterApproval(t *testing.T) {
	_, store, rules, home := testRememberTool(t)
	skills := skill.Discover(home)
	runtime, err := NewRuntime(home)
	if err != nil {
		t.Fatal(err)
	}
	base, err := NewDefaultRegistryWithMemory(runtime, skills, store, rules, "session-test")
	if err != nil {
		t.Fatal(err)
	}
	projectRules, err := NewProjectRules(runtime.WorkDir, runtime.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	gate := NewApprovalGate()
	approver := &fakeApprover{}
	gate.Set(func(ctx context.Context, request ApprovalRequest) (ApprovalDecision, error) {
		approver.mu.Lock()
		defer approver.mu.Unlock()
		return approver.decision, approver.err
	})
	permissioned, err := NewDefault(base, runtime, projectRules, gate)
	if err != nil {
		t.Fatal(err)
	}
	approver.setDecision(ApprovalDecision{Allow: true})
	result, err := permissioned.Execute(context.Background(), model.ToolCall{
		Name: "remember", Arguments: json.RawMessage(`{"target":"project_memory","name":"approved","description":"审批后写入","body":"正文"}`),
	})
	if err != nil || !strings.Contains(result.Content, "已记录项目记忆") {
		t.Fatalf("审批通过后写入失败：%q %v", result.Content, err)
	}
	if _, err := store.Read(memory.ScopeProject, "approved"); err != nil {
		t.Fatalf("写入结果不存在：%v", err)
	}
	approver.setDecision(ApprovalDecision{Allow: false})
	_, err = permissioned.Execute(context.Background(), model.ToolCall{
		Name: "remember", Arguments: json.RawMessage(`{"target":"project_memory","name":"denied","description":"拒绝","body":"正文"}`),
	})
	if err == nil {
		t.Fatal("审批拒绝后不应写入")
	}
	if _, err := store.Read(memory.ScopeProject, "denied"); err == nil {
		t.Fatal("拒绝后不应存在记忆")
	}
}

func TestRememberFilePath(t *testing.T) {
	tool, _, rules, home := testRememberTool(t)
	if _, err := executeRemember(t, tool, `{"target":"user_agents","name":"rule","description":"规则","confidence":"high","evidence":"用户明确要求"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".johnny", "AGENTS.md")); err != nil {
		t.Fatalf("用户 AGENTS 未写入：%v", err)
	}
	if _, err := os.Stat(rules.Path()); err != nil {
		t.Fatalf("规则路径错误：%v", err)
	}
}
