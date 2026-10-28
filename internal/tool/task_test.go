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

type fakeSpawner struct {
	requests []SpawnRequest
	origins  []Origin
	id       string
	err      error
}

func (f *fakeSpawner) Spawn(ctx context.Context, request SpawnRequest) (string, error) {
	f.requests = append(f.requests, request)
	f.origins = append(f.origins, OriginFrom(ctx))
	if f.err != nil {
		return "", f.err
	}
	if f.id == "" {
		return "ab12", nil
	}
	return f.id, nil
}

func taskTestTool(t *testing.T) (*TaskTool, *fakeSpawner, Runtime) {
	t.Helper()
	runtime := testRuntime(t)
	spawner := &fakeSpawner{}
	taskTool, err := NewTaskTool(runtime, spawner)
	if err != nil {
		t.Fatal(err)
	}
	return taskTool, spawner, runtime
}

func runTask(taskTool *TaskTool, arguments string) (Result, error) {
	return taskTool.Execute(context.Background(), json.RawMessage(arguments))
}

func TestTaskToolStartsFreshReadTaskWithDefaults(t *testing.T) {
	taskTool, spawner, _ := taskTestTool(t)
	result, err := runTask(taskTool, `{"description":"查日志","prompt":"  找出失败原因  ","kind":"fresh"}`)
	if err != nil || result.IsError {
		t.Fatalf("task 应成功：%+v %v", result, err)
	}
	if len(spawner.requests) != 1 {
		t.Fatal("应启动一个子任务")
	}
	request := spawner.requests[0]
	if request.Description != "查日志" || request.Prompt != "找出失败原因" || request.Kind != SpawnFresh || request.Access != SpawnRead || request.Paths != nil {
		t.Fatalf("子任务请求错误：%+v", request)
	}
	for _, want := range []string{"ab12", "查日志", "不需要等待"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("返回内容缺少 %q：%s", want, result.Content)
		}
	}
}

func TestTaskToolResolvesWritePathsInsideWorkdir(t *testing.T) {
	taskTool, spawner, runtime := taskTestTool(t)
	if err := os.MkdirAll(filepath.Join(runtime.WorkDir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runTask(taskTool, `{"description":"重构","prompt":"改","kind":"fork","access":"write","paths":["pkg","pkg","./new/file.go"]}`); err != nil {
		t.Fatal(err)
	}
	request := spawner.requests[0]
	root, err := filepath.EvalSymlinks(runtime.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, "pkg"), filepath.Join(root, "new", "file.go")}
	if request.Kind != SpawnFork || request.Access != SpawnWrite || strings.Join(request.Paths, "|") != strings.Join(want, "|") {
		t.Fatalf("写入范围未规范化或未去重：%+v", request)
	}
}

func TestTaskToolRejectsInvalidArguments(t *testing.T) {
	taskTool, spawner, _ := taskTestTool(t)
	outside := filepath.Join(t.TempDir(), "x")
	cases := map[string]string{
		"缺少描述":    `{"prompt":"p","kind":"fresh"}`,
		"描述过长":    `{"description":"` + strings.Repeat("长", 81) + `","prompt":"p","kind":"fresh"}`,
		"缺少任务说明":  `{"description":"d","kind":"fresh"}`,
		"缺少 kind": `{"description":"d","prompt":"p"}`,
		"未知 kind": `{"description":"d","prompt":"p","kind":"clone"}`,
		"未知访问级别":  `{"description":"d","prompt":"p","kind":"fresh","access":"admin"}`,
		"写入缺少范围":  `{"description":"d","prompt":"p","kind":"fresh","access":"write"}`,
		"读取带范围":   `{"description":"d","prompt":"p","kind":"fresh","paths":["a"]}`,
		"范围在外部":   `{"description":"d","prompt":"p","kind":"fresh","access":"write","paths":["` + outside + `"]}`,
		"范围用 ..":  `{"description":"d","prompt":"p","kind":"fresh","access":"write","paths":["../x"]}`,
		"参数不是对象":  `[]`,
	}
	for name, arguments := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := runTask(taskTool, arguments)
			var toolErr *Error
			if !errors.As(err, &toolErr) || toolErr.Kind != KindInvalidArguments {
				t.Fatalf("应返回参数错误：%v", err)
			}
		})
	}
	if len(spawner.requests) != 0 {
		t.Fatal("无效参数不应启动子任务")
	}
}

func TestTaskToolReportsSpawnerErrorsAndKeepsCancellation(t *testing.T) {
	taskTool, spawner, _ := taskTestTool(t)
	spawner.err = errors.New("同时最多 5 个子任务")
	result, err := runTask(taskTool, `{"description":"d","prompt":"p","kind":"fresh"}`)
	var toolErr *Error
	if !errors.As(err, &toolErr) || toolErr.Kind != KindExecution || !strings.Contains(err.Error(), "最多 5 个") {
		t.Fatalf("启动失败应作为执行错误返回：%+v %v", result, err)
	}
	if converted := ToModelResult("call", result, err); !converted.IsError || !strings.Contains(converted.Content, "最多 5 个") {
		t.Fatalf("模型应看到失败原因：%+v", converted)
	}
	spawner.err = context.Canceled
	if _, err := runTask(taskTool, `{"description":"d","prompt":"p","kind":"fresh"}`); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误应原样返回：%v", err)
	}
}

func TestTaskToolPassesContextToSpawner(t *testing.T) {
	taskTool, spawner, _ := taskTestTool(t)
	ctx := WithOrigin(context.Background(), Origin{ID: "parent"})
	if _, err := taskTool.Execute(ctx, json.RawMessage(`{"description":"d","prompt":"p","kind":"fresh"}`)); err != nil {
		t.Fatal(err)
	}
	if spawner.origins[0].ID != "parent" {
		t.Fatal("Spawner 应收到调用方 context")
	}
}

func TestTaskToolRequiresSpawnerAndRegisters(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := NewTaskTool(runtime, nil); err == nil {
		t.Fatal("缺少 Spawner 应失败")
	}
	registry, err := NewDefaultRegistry(runtime)
	if err != nil {
		t.Fatal(err)
	}
	taskTool, err := NewTaskTool(runtime, &fakeSpawner{})
	if err != nil {
		t.Fatal(err)
	}
	extended, err := registry.With(taskTool)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Definitions()) != 4 || len(extended.Definitions()) != 5 {
		t.Fatalf("With 不应改动原注册表：%d %d", len(registry.Definitions()), len(extended.Definitions()))
	}
	if _, ok := extended.Get("task"); !ok {
		t.Fatal("扩展后的注册表应包含 task")
	}
	if _, err := extended.With(taskTool); err == nil {
		t.Fatal("重复注册 task 应失败")
	}
}

func TestDefaultAndReadOnlyPassTaskThroughWithoutApproval(t *testing.T) {
	runtime := testRuntime(t)
	spawner := &fakeSpawner{}
	taskTool, err := NewTaskTool(runtime, spawner)
	if err != nil {
		t.Fatal(err)
	}
	base, err := NewDefaultRegistry(runtime)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := base.With(taskTool)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := NewProjectRules(runtime.WorkDir, runtime.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	gate := NewApprovalGate()
	gate.Set(func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		t.Fatal("派出子任务本身不应触发审批")
		return ApprovalDecision{}, nil
	})
	permissioned, err := NewDefault(registry, runtime, rules, gate)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := NewReadOnly(registry, runtime)
	if err != nil {
		t.Fatal(err)
	}
	call := model.ToolCall{ID: "task-1", Name: "task", Arguments: json.RawMessage(`{"description":"d","prompt":"p","kind":"fresh"}`)}
	for name, executor := range map[string]interface {
		Execute(context.Context, model.ToolCall) (Result, error)
		Definitions() []model.ToolDefinition
	}{"default": permissioned, "read-only": readOnly} {
		if _, err := executor.Execute(context.Background(), call); err != nil {
			t.Fatalf("%s 模式应放行 task：%v", name, err)
		}
		found := false
		for _, definition := range executor.Definitions() {
			found = found || definition.Name == "task"
		}
		if !found {
			t.Fatalf("%s 模式应暴露 task 工具", name)
		}
	}
	if len(spawner.requests) != 2 {
		t.Fatalf("两种模式都应启动子任务：%d", len(spawner.requests))
	}
	for _, definition := range readOnly.Definitions() {
		if definition.Name == "task" && !strings.Contains(definition.Description, "access=read") {
			t.Fatal("只读模式应在描述中说明只能派出只读子任务")
		}
	}
}

func TestApprovalRequestAndAuditCarryOrigin(t *testing.T) {
	permissioned, runtime, _, approver := defaultTest(t)
	approver.setDecision(ApprovalDecision{Allow: true})
	var audits []ApprovalAudit
	permissioned.gate.SetAudit(func(audit ApprovalAudit) { audits = append(audits, audit) })
	outside := filepath.Join(t.TempDir(), "outside.txt")
	raw, err := json.Marshal(map[string]any{"path": outside, "content": "x"})
	if err != nil {
		t.Fatal(err)
	}
	child := WithOrigin(context.Background(), Origin{ID: "ab12", Description: "写文档"})
	if _, err := permissioned.Execute(child, model.ToolCall{ID: "w1", Name: "write", Arguments: raw}); err != nil {
		t.Fatal(err)
	}
	if _, err := permissioned.Execute(context.Background(), model.ToolCall{ID: "w2", Name: "write", Arguments: raw}); err != nil {
		t.Fatal(err)
	}
	requests := approver.snapshot()
	if len(requests) != 2 || requests[0].Origin.ID != "ab12" || requests[0].Origin.Description != "写文档" || !requests[1].Origin.IsZero() {
		t.Fatalf("审批请求应标明来源：%+v", requests)
	}
	if len(audits) != 2 || audits[0].Origin.ID != "ab12" || !audits[1].Origin.IsZero() {
		t.Fatalf("审计记录应标明来源：%+v", audits)
	}
	_ = runtime
}
