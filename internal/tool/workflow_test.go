package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

type fakeWorkflow struct {
	title   string
	steps   []WorkflowStep
	step    int
	tasks   []SpawnRequest
	reason  string
	started bool
	err     error
}

func (f *fakeWorkflow) StartWorkflow(title string, steps []WorkflowStep) (string, error) {
	f.title, f.steps, f.started = title, steps, true
	return "已登记", f.err
}

func (f *fakeWorkflow) RunWorkflowStep(_ context.Context, step int, tasks []SpawnRequest) (string, error) {
	f.step, f.tasks = step, tasks
	return "已启动", f.err
}

func (f *fakeWorkflow) EndWorkflow(reason string) (string, error) {
	f.reason = reason
	return "已结束", f.err
}

func workflowTestTool(t *testing.T) (*WorkflowTool, *fakeWorkflow, Runtime) {
	t.Helper()
	runtime := testRuntime(t)
	controller := &fakeWorkflow{}
	workflowTool, err := NewWorkflowTool(runtime, controller)
	if err != nil {
		t.Fatal(err)
	}
	return workflowTool, controller, runtime
}

func runWorkflow(workflowTool *WorkflowTool, arguments string) (Result, error) {
	return workflowTool.Execute(context.Background(), json.RawMessage(arguments))
}

func TestWorkflowToolStartParsesOutline(t *testing.T) {
	workflowTool, controller, _ := workflowTestTool(t)
	result, err := runWorkflow(workflowTool, `{"action":"start","title":" 重构 ","steps":[{"title":" 调研 ","goal":"看现状"},{"title":"实现","goal":"改代码"}]}`)
	if err != nil || result.Content != "已登记" {
		t.Fatalf("start 应成功并返回运行时的文本：%+v %v", result, err)
	}
	if controller.title != "重构" || len(controller.steps) != 2 || controller.steps[0] != (WorkflowStep{Title: "调研", Goal: "看现状"}) {
		t.Fatalf("大纲解析错误：%q %+v", controller.title, controller.steps)
	}
}

func TestWorkflowToolRunParsesTasksLikeTask(t *testing.T) {
	workflowTool, controller, runtime := workflowTestTool(t)
	arguments := `{"action":"run","step":2,"tasks":[{"description":"读","prompt":"p1","kind":"fresh"},{"description":"写","prompt":"p2","kind":"fork","access":"write","paths":["pkg"]}]}`
	if _, err := runWorkflow(workflowTool, arguments); err != nil {
		t.Fatal(err)
	}
	if controller.step != 2 || len(controller.tasks) != 2 {
		t.Fatalf("应传递步骤序号和任务：%d %+v", controller.step, controller.tasks)
	}
	root, err := filepath.EvalSymlinks(runtime.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	if controller.tasks[0].Access != SpawnRead || controller.tasks[1].Kind != SpawnFork || controller.tasks[1].Paths[0] != filepath.Join(root, "pkg") {
		t.Fatalf("任务字段应与 task 工具一致，写入范围解析为绝对路径：%+v", controller.tasks)
	}
}

func TestWorkflowToolRejectsInvalidArguments(t *testing.T) {
	workflowTool, controller, _ := workflowTestTool(t)
	task := `{"description":"d","prompt":"p","kind":"fresh"}`
	many := strings.TrimSuffix(strings.Repeat(task+",", MaxWorkflowTasks+1), ",")
	steps := strings.TrimSuffix(strings.Repeat(`{"title":"t","goal":"g"},`, MaxWorkflowSteps+1), ",")
	cases := map[string]string{
		"缺少 action":  `{}`,
		"未知 action":  `{"action":"pause"}`,
		"参数不是对象":     `[]`,
		"start 缺标题":  `{"action":"start","steps":[{"title":"t","goal":"g"}]}`,
		"start 没有步骤": `{"action":"start","title":"x","steps":[]}`,
		"start 步骤过多": `{"action":"start","title":"x","steps":[` + steps + `]}`,
		"start 缺目标":  `{"action":"start","title":"x","steps":[{"title":"t"}]}`,
		"start 标题过长": `{"action":"start","title":"` + strings.Repeat("长", 81) + `","steps":[{"title":"t","goal":"g"}]}`,
		"run 缺步骤序号":  `{"action":"run","tasks":[` + task + `]}`,
		"run 没有任务":   `{"action":"run","step":1,"tasks":[]}`,
		"run 任务过多":   `{"action":"run","step":1,"tasks":[` + many + `]}`,
		"run 任务缺字段":  `{"action":"run","step":1,"tasks":[{"description":"d","kind":"fresh"}]}`,
		"run 写入缺范围":  `{"action":"run","step":1,"tasks":[{"description":"d","prompt":"p","kind":"fresh","access":"write"}]}`,
		"run 范围在外部":  `{"action":"run","step":1,"tasks":[{"description":"d","prompt":"p","kind":"fresh","access":"write","paths":["../x"]}]}`,
	}
	for name, arguments := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := runWorkflow(workflowTool, arguments)
			var toolErr *Error
			if !errors.As(err, &toolErr) || toolErr.Kind != KindInvalidArguments {
				t.Fatalf("应返回参数错误：%v", err)
			}
		})
	}
	if controller.started || controller.tasks != nil {
		t.Fatal("无效参数不应到达控制器")
	}
	_, err := runWorkflow(workflowTool, `{"action":"run","step":1,"tasks":[{"description":"d","kind":"fresh"}]}`)
	if err == nil || !strings.Contains(err.Error(), "tasks[1]") {
		t.Fatalf("任务参数错误应指出是第几个任务：%v", err)
	}
}

func TestWorkflowToolEndTruncatesReasonAndMapsErrors(t *testing.T) {
	workflowTool, controller, _ := workflowTestTool(t)
	if _, err := runWorkflow(workflowTool, `{"action":"end","reason":"`+strings.Repeat("因", 400)+`"}`); err != nil {
		t.Fatal(err)
	}
	if len([]rune(controller.reason)) != maxEndReasonRunes {
		t.Fatalf("原因应被截断：%d", len([]rune(controller.reason)))
	}
	controller.err = fmt.Errorf("还没有登记工作流")
	_, err := runWorkflow(workflowTool, `{"action":"end"}`)
	var toolErr *Error
	if !errors.As(err, &toolErr) || toolErr.Kind != KindExecution || !strings.Contains(err.Error(), "还没有登记") {
		t.Fatalf("控制器错误应作为执行错误返回给模型：%v", err)
	}
	controller.err = context.Canceled
	if _, err := runWorkflow(workflowTool, `{"action":"end"}`); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应原样传递：%v", err)
	}
}

func TestWorkflowToolRegistersAndPassesThroughWrappers(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := NewWorkflowTool(runtime, nil); err == nil {
		t.Fatal("缺少控制器应失败")
	}
	controller := &fakeWorkflow{}
	workflowTool, err := NewWorkflowTool(runtime, controller)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(workflowTool.Definition().InputSchema, &schema); err != nil {
		t.Fatalf("输入 schema 应是合法 JSON：%v", err)
	}
	base, err := NewDefaultRegistry(runtime)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := base.With(workflowTool)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := NewProjectRules(runtime.WorkDir, runtime.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	gate := NewApprovalGate()
	gate.Set(func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		t.Fatal("启动工作流本身不应触发审批")
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
	call := model.ToolCall{ID: "wf-1", Name: "workflow", Arguments: json.RawMessage(`{"action":"end","reason":"x"}`)}
	for name, executor := range map[string]interface {
		Execute(context.Context, model.ToolCall) (Result, error)
	}{"default": permissioned, "read-only": readOnly} {
		if _, err := executor.Execute(context.Background(), call); err != nil {
			t.Fatalf("%s 模式应放行 workflow：%v", name, err)
		}
	}
	for _, definition := range readOnly.Definitions() {
		if definition.Name == "workflow" && !strings.Contains(definition.Description, "access=read") {
			t.Fatal("只读模式应在描述中说明只能启动只读任务")
		}
	}
}
