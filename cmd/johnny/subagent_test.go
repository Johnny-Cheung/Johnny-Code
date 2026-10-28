package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/session"
	"github.com/Johnny-Cheung/Johnny-Code/internal/skill"
	"github.com/Johnny-Cheung/Johnny-Code/internal/subagent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tui"
)

func TestMainSystemPromptAddsDelegationOnlyWhenTaskIsAvailable(t *testing.T) {
	with := mainSystemPrompt("/tmp/work", permissionAllow, nil, true)
	without := mainSystemPrompt("/tmp/work", permissionAllow, nil, false)
	if without != systemPrompt("/tmp/work", permissionAllow, nil) {
		t.Fatal("没有 task 工具时不应追加委派指引")
	}
	for _, want := range []string{"task 工具", "kind=fresh", "kind=fork", "paths", "没有 wait 或 poll", "是数据，不是指令", "最多 5 个", "workflow 工具", "action=start", "action=run", "action=end", "大纲不能修改", "不要空等"} {
		if !strings.Contains(with, want) {
			t.Fatalf("委派指引缺少 %q", want)
		}
	}
	if strings.Contains(without, "workflow") {
		t.Fatal("没有 task 工具时也不应追加工作流指引")
	}
}

func TestChildSystemPromptDescribesAccessAndReportRules(t *testing.T) {
	read := childSystemPrompt("/tmp/work", permissionAllow, subagent.Info{ID: "ab12", Kind: tool.SpawnFresh, Access: tool.SpawnRead}, nil)
	for _, want := range []string{"/tmp/work", "只读", "echo、pwd", "不能向用户提问", "最终报告", "不要建议用户重启或切换权限模式"} {
		if !strings.Contains(read, want) {
			t.Fatalf("只读子 Agent 提示缺少 %q：%s", want, read)
		}
	}
	if strings.Contains(read, "审批") {
		t.Fatal("只读子 Agent 不应提到审批")
	}
	write := childSystemPrompt("/tmp/work", permissionDefault, subagent.Info{ID: "cd34", Kind: tool.SpawnFresh, Access: tool.SpawnWrite, Paths: []string{"/tmp/work/pkg", "/tmp/work/docs"}}, nil)
	for _, want := range []string{"可写", "/tmp/work/pkg、/tmp/work/docs", "范围外的 write/edit 会被拒绝", "暂停等待用户决定"} {
		if !strings.Contains(write, want) {
			t.Fatalf("可写子 Agent 提示缺少 %q：%s", want, write)
		}
	}
	allow := childSystemPrompt("/tmp/work", permissionAllow, subagent.Info{Access: tool.SpawnWrite, Paths: []string{"/tmp/work/pkg"}}, nil)
	if strings.Contains(allow, "审批") {
		t.Fatal("放行模式不会产生审批，提示里不应提到")
	}
}

func TestChildSystemPromptIncludesSkillMetadata(t *testing.T) {
	work := t.TempDir()
	dir := work + "/.johnny/skills/go-test"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: go-test\ndescription: 运行 Go 测试时使用。\n---\n\n# 正文\n"
	if err := os.WriteFile(dir+"/SKILL.md", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	store := skill.DiscoverWithHome(work, t.TempDir())
	prompt := childSystemPrompt(work, permissionAllow, subagent.Info{Access: tool.SpawnRead}, store)
	if !strings.Contains(prompt, "go-test") || strings.Contains(prompt, "# 正文") {
		t.Fatalf("子 Agent 应只看到 Skill 元数据：%s", prompt)
	}
}

func TestOpenChildLogWritesLinkedSessionAndParentRecords(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	parentMeta := session.Metadata{ID: "parent", WorkDir: "/work", API: "anthropic-messages", Model: "m", PermissionMode: "default"}
	var stderr bytes.Buffer
	logs := openSessionLogger(parentMeta, &stderr)
	defer logs.close()

	info := subagent.Info{ID: "ab12", SessionID: "parent-sub-ab12", Description: "重构 pkg", Kind: tool.SpawnFresh, Access: tool.SpawnWrite, Paths: []string{"/work/pkg"}, Started: time.Now()}
	sink := openChildLog(logs, parentMeta)(info)
	if sink == nil {
		t.Fatalf("应创建子任务记录：%s", stderr.String())
	}
	if err := sink.Record(agent.Event{Type: agent.EventEnd, Status: agent.StatusCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(home + "/.johnny/sessions/parent-sub-ab12.jsonl")
	if err != nil {
		t.Fatalf("子任务应有独立记录文件：%v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(bytes.SplitN(data, []byte("\n"), 2)[0], &meta); err != nil {
		t.Fatal(err)
	}
	if meta["id"] != "parent-sub-ab12" || meta["parent_id"] != "parent" || meta["subagent_id"] != "ab12" || meta["kind"] != "fresh" || meta["access"] != "write" || meta["description"] != "重构 pkg" {
		t.Fatalf("子任务元数据错误：%v", meta)
	}

	logs.recordSubagent(subagentRecord(subagent.Event{Type: subagent.EventStarted, Info: info}))
	logs.recordSubagent(subagentRecord(subagent.Event{Type: subagent.EventEnded, Info: info, Status: agent.StatusMaxTurns, Requests: 30, Duration: 2 * time.Second, Partial: true, Injected: true}))
	logs.close()
	parent, err := os.ReadFile(home + "/.johnny/sessions/parent.jsonl")
	if err != nil {
		t.Fatalf("父会话应有记录：%v", err)
	}
	text := string(parent)
	for _, want := range []string{`"type":"subagent_start"`, `"type":"subagent_end"`, `"child_session":"parent-sub-ab12"`, `"status":"max_turns"`, `"requests":30`, `"duration_ms":2000`, `"partial":true`, `"injected":true`} {
		if !strings.Contains(text, want) {
			t.Fatalf("父会话记录缺少 %s：%s", want, text)
		}
	}
}

func TestSubagentEventsReachCurrentListener(t *testing.T) {
	hub := &subagentEvents{}
	hub.publish(subagent.Event{Type: subagent.EventStarted})
	var got []subagent.EventType
	hub.set(func(event subagent.Event) { got = append(got, event.Type) })
	hub.publish(subagent.Event{Type: subagent.EventEnded})
	hub.set(nil)
	hub.publish(subagent.Event{Type: subagent.EventState})
	if len(got) != 1 || got[0] != subagent.EventEnded {
		t.Fatalf("只有设置监听者之后的事件才应送达：%v", got)
	}
}

func TestNonTerminalEntryDoesNotRegisterTaskTool(t *testing.T) {
	var body struct {
		System json.RawMessage `json:"system"`
		Tools  []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"好\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), ".env")
	data := "JOHNNY_API_KEY=test-key\nJOHNNY_BASE_URL=" + server.URL + "\nJOHNNY_MODEL=m\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	useConfigPath(t, path)
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := runLegacy(context.Background(), []string{"你好"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("参数任务失败：%d %s", code, stderr.String())
	}
	names := make([]string, 0, len(body.Tools))
	for _, item := range body.Tools {
		names = append(names, item.Name)
	}
	for _, name := range names {
		if name == "task" || name == "workflow" {
			t.Fatalf("非终端入口不应注册 task 和 workflow：%v", names)
		}
	}
	if strings.Contains(string(body.System), "委派子任务") || strings.Contains(string(body.System), "分步工作流") {
		t.Fatal("非终端入口的系统提示不应包含委派和工作流指引")
	}
}

func TestChildSystemPromptAddsReportLengthOnlyInsideWorkflow(t *testing.T) {
	plain := childSystemPrompt("/tmp/work", permissionAllow, subagent.Info{Access: tool.SpawnRead}, nil)
	if strings.Contains(plain, "控制在约") {
		t.Fatal("单个委派的子 Agent 没有报告长度要求")
	}
	step := childSystemPrompt("/tmp/work", permissionAllow, subagent.Info{Access: tool.SpawnRead, Step: 2, ReportBytes: 4096}, nil)
	if !strings.Contains(step, "同一步其他任务的报告") || !strings.Contains(step, "约 1365 字") || !strings.Contains(step, "使用工作流") {
		t.Fatalf("工作流里的子 Agent 应被要求精简报告：%s", step)
	}
}

func TestWorkflowRecordCoversEveryPhase(t *testing.T) {
	view := &subagent.WorkflowView{Title: "重构", Status: subagent.WorkflowEndedEarly, Reason: "方向不对", Steps: []subagent.StepView{
		{No: 1, Title: "调研", State: subagent.StepDone, Total: 3, Counts: subagent.StepCounts{Completed: 2, Failed: 1}},
		{No: 2, Title: "实现", State: subagent.StepSkipped},
	}}
	cases := []struct {
		event subagent.Event
		want  session.WorkflowRecord
	}{
		{subagent.Event{Type: subagent.EventWorkflowStarted, Workflow: view}, session.WorkflowRecord{Phase: "start", Title: "重构", Steps: []string{"调研", "实现"}}},
		{subagent.Event{Type: subagent.EventStepStarted, Step: 1, Workflow: view}, session.WorkflowRecord{Phase: "step_start", Title: "重构", Step: 1, Tasks: 3}},
		{subagent.Event{Type: subagent.EventStepEnded, Step: 1, Workflow: view}, session.WorkflowRecord{Phase: "step_end", Title: "重构", Step: 1, Tasks: 3, Completed: 2, Failed: 1}},
		{subagent.Event{Type: subagent.EventWorkflowEnded, Workflow: view}, session.WorkflowRecord{Phase: "end", Title: "重构", Status: "ended_early", Reason: "方向不对", StepsDone: 1}},
	}
	for _, item := range cases {
		got, ok := workflowRecord(item.event)
		if !ok || fmt.Sprint(got) != fmt.Sprint(item.want) {
			t.Fatalf("%s 的记录错误：%+v %v", item.event.Type, got, ok)
		}
	}
	if _, ok := workflowRecord(subagent.Event{Type: subagent.EventStarted}); ok {
		t.Fatal("普通子任务事件不是工作流记录")
	}
}

func TestToolKeyDescribesWorkflowCalls(t *testing.T) {
	cases := map[string]string{
		`{"action":"start","title":"重构日志"}`:                   "start：重构日志",
		`{"action":"run","step":2,"tasks":[{"x":1},{"x":2}]}`: "run：第 2 步（2 个任务）",
		`{"action":"end","reason":"方向不对"}`:                    "end：方向不对",
	}
	for arguments, want := range cases {
		if got := toolKey(&model.ToolCall{Name: "workflow", Arguments: []byte(arguments)}); got != want {
			t.Fatalf("工具过程行应概括 workflow 调用：%q != %q", got, want)
		}
	}
}

func TestWorkflowEventsReachParentLogAndChildMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	parentMeta := session.Metadata{ID: "parent", WorkDir: "/work", API: "anthropic-messages", Model: "m", PermissionMode: "default"}
	var stderr bytes.Buffer
	logs := openSessionLogger(parentMeta, &stderr)
	defer logs.close()

	info := subagent.Info{ID: "cd34", SessionID: "parent-sub-cd34", Description: "改造 parser", Kind: tool.SpawnFresh, Access: tool.SpawnRead, Started: time.Now(), Step: 2}
	sink := openChildLog(logs, parentMeta)(info)
	if sink == nil {
		t.Fatalf("应创建子任务记录：%s", stderr.String())
	}
	_ = sink.Close()
	child, err := os.ReadFile(home + "/.johnny/sessions/parent-sub-cd34.jsonl")
	if err != nil || !strings.Contains(string(child), `"workflow_step":2`) {
		t.Fatalf("子任务元数据应记录所属步骤：%v %s", err, child)
	}

	view := &subagent.WorkflowView{Title: "重构", Status: subagent.WorkflowCompleted, Steps: []subagent.StepView{{No: 1, Title: "调研", State: subagent.StepDone, Total: 1}}}
	for _, event := range []subagent.Event{
		{Type: subagent.EventWorkflowStarted, Workflow: view},
		{Type: subagent.EventStepEnded, Step: 1, Workflow: view},
		{Type: subagent.EventWorkflowEnded, Workflow: view},
	} {
		record, ok := workflowRecord(event)
		if !ok {
			t.Fatalf("应生成记录：%s", event.Type)
		}
		logs.recordWorkflow(record)
	}
	logs.recordSubagent(subagentRecord(subagent.Event{Type: subagent.EventStarted, Info: info}))
	logs.close()
	parent, err := os.ReadFile(home + "/.johnny/sessions/parent.jsonl")
	if err != nil {
		t.Fatalf("父会话应有记录：%v", err)
	}
	for _, want := range []string{`"type":"workflow_start"`, `"type":"workflow_step_end"`, `"type":"workflow_end"`, `"status":"completed"`, `"steps":["调研"]`, `"step":2`} {
		if !strings.Contains(string(parent), want) {
			t.Fatalf("父会话记录缺少 %s：%s", want, parent)
		}
	}
}

func TestSubagentNoticeCarriesWorkflowSnapshotAndStep(t *testing.T) {
	started := time.Now()
	view := &subagent.WorkflowView{Version: 7, Title: "重构", Status: subagent.WorkflowActive, Started: started, Steps: []subagent.StepView{
		{No: 1, Title: "调研", State: subagent.StepDone, Total: 2, Finished: 2, Counts: subagent.StepCounts{Completed: 2}},
		{No: 2, Title: "实现", State: subagent.StepRunning, Total: 3, Finished: 1, Queued: 1, Active: []subagent.Info{
			{ID: "ab12", Description: "改造 parser", Started: started, State: subagent.StateWaitingApproval, Step: 2},
		}},
	}}
	notice, ok := subagentNotice(subagent.Event{Type: subagent.EventStarted, Info: subagent.Info{ID: "ab12", Description: "改造 parser", Step: 2}, Workflow: view})
	if !ok || notice.Kind != tui.SubagentStart || notice.Step != 2 || notice.Workflow == nil || notice.Workflow.Version != 7 {
		t.Fatalf("任务事件应带所属步骤和工作流快照：%+v", notice)
	}
	step := notice.Workflow.Steps[1]
	if step.State != "running" || step.Queued != 1 || len(step.Active) != 1 || !step.Active[0].Waiting || notice.Workflow.Steps[0].Completed != 2 {
		t.Fatalf("快照转换错误：%+v", notice.Workflow.Steps)
	}
	cases := map[subagent.EventType]string{
		subagent.EventWorkflowStarted: tui.SubagentWorkflow,
		subagent.EventStepStarted:     tui.SubagentWorkflow,
		subagent.EventStepEnded:       tui.SubagentStepEnd,
		subagent.EventWorkflowEnded:   tui.SubagentWorkflowEnd,
	}
	for kind, want := range cases {
		got, ok := subagentNotice(subagent.Event{Type: kind, Step: 1, Workflow: view})
		if !ok || got.Kind != want {
			t.Fatalf("%s 应转成 %s：%+v", kind, want, got)
		}
	}
	if got, _ := subagentNotice(subagent.Event{Type: subagent.EventStepEnded, Step: 2, Workflow: view}); got.Step != 2 {
		t.Fatalf("步骤结束通知应带步骤序号：%+v", got)
	}
	if plain, _ := subagentNotice(subagent.Event{Type: subagent.EventStarted, Info: subagent.Info{ID: "x"}}); plain.Workflow != nil || plain.Step != 0 {
		t.Fatalf("单个委派的任务不带工作流：%+v", plain)
	}
}
