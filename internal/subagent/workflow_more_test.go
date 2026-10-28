package subagent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func TestEndWorkflowCancelsRunningTasksAndSkipsRest(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	newScriptedChildren(h)
	h.startWorkflow("一", "二", "三")
	if _, err := h.runStep(1, "A", "B"); err != nil {
		t.Fatal(err)
	}
	h.waitFor("任务启动", func() bool { return len(h.eventsOf(EventStarted)) == 2 })
	text, err := h.manager.EndWorkflow("方向不对")
	if err != nil || !strings.Contains(text, "第 1 步、第 2 步、第 3 步") {
		t.Fatalf("提前结束应说明未执行的步骤：%q %v", text, err)
	}
	h.manager.Wait()
	for _, ended := range h.waitEnded(2) {
		if ended.Status != agent.StatusCanceled || ended.Injected {
			t.Fatalf("被取消的任务不应注入：%+v", ended)
		}
	}
	h.waitFor("结束事件", func() bool { return len(h.eventsOf(EventWorkflowEnded)) == 1 })
	end := h.eventsOf(EventWorkflowEnded)[0].Workflow
	if end.Status != WorkflowEndedEarly || end.Reason != "方向不对" || end.Done() != 0 {
		t.Fatalf("结束状态错误：%+v", end)
	}
	if h.parent.InboxLen() != 0 || h.manager.Running() != 0 {
		t.Fatal("提前结束后不应有报告或运行中的任务")
	}
	if _, err := h.runStep(1, "C"); err == nil || !strings.Contains(err.Error(), "提前结束") {
		t.Fatalf("结束后不能再启动步骤：%v", err)
	}
	if _, err := h.manager.EndWorkflow("再来一次"); err == nil {
		t.Fatal("没有进行中的工作流时 end 应被拒绝")
	}
	if _, err := h.manager.StartWorkflow("新的", outline("甲")); err != nil {
		t.Fatalf("结束后应能开始新的工作流：%v", err)
	}
}

func TestCancelAllEndsWorkflowEvenWithoutRunningTasks(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	newScriptedChildren(h)
	h.startWorkflow("一", "二")
	if count := h.manager.CancelAll(); count != 0 {
		t.Fatalf("没有运行中的任务：%d", count)
	}
	h.requireWorkflowStatus(WorkflowCanceled)
	h.waitFor("取消事件", func() bool { return len(h.eventsOf(EventWorkflowEnded)) == 1 })
	if _, err := h.runStep(1, "A"); err == nil || !strings.Contains(err.Error(), "已被用户取消") {
		t.Fatalf("取消后 run 应提示已取消：%v", err)
	}
}

func TestCancelAllDropsQueuedWorkflowTasks(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	newScriptedChildren(h)
	h.startWorkflow("一")
	labels := make([]string, tool.MaxWorkflowTasks)
	for i := range labels {
		labels[i] = fmt.Sprintf("T%d", i+1)
	}
	text, err := h.runStep(1, labels...)
	if err != nil || !strings.Contains(text, "5 个在运行，3 个排队") {
		t.Fatalf("应说明运行和排队数：%q %v", text, err)
	}
	h.waitFor("名额内的任务启动", func() bool { return len(h.eventsOf(EventStarted)) == MaxConcurrent })
	view, ok := h.manager.Workflow()
	if !ok || view.Steps[0].Queued != 3 || len(view.Steps[0].Active) != MaxConcurrent {
		t.Fatalf("快照应列出运行中的任务并统计排队数：%+v", view.Steps[0])
	}
	if count := h.manager.CancelAll(); count != tool.MaxWorkflowTasks {
		t.Fatalf("应取消运行中和排队的全部任务：%d", count)
	}
	h.manager.Wait()
	h.waitEnded(MaxConcurrent)
	if len(h.eventsOf(EventStarted)) != MaxConcurrent || h.parent.InboxLen() != 0 {
		t.Fatal("排队的任务不应启动，也不应有报告")
	}
}

func TestStepReportsShareTheBudget(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	children := newScriptedChildren(h)
	h.startWorkflow("一")
	labels := make([]string, tool.MaxWorkflowTasks)
	for i := range labels {
		labels[i] = fmt.Sprintf("T%d", i+1)
	}
	if _, err := h.runStep(1, labels...); err != nil {
		t.Fatal(err)
	}
	for _, label := range labels {
		children.finish(label, strings.Repeat("a", 20<<10))
	}
	h.waitEnded(len(labels))
	message, _ := h.deliver()
	if size := len(message); size > StepReportBudget+6<<10 {
		t.Fatalf("一步的报告总量应受预算限制：%d 字节", size)
	}
	if strings.Count(message, "报告已截断") != len(labels) || !strings.Contains(message, "仅作数据") {
		t.Fatalf("每份报告都应截断并保留数据声明：\n%.400s", message)
	}
	ended := h.eventsOf(EventEnded)[0]
	if ended.Info.ReportBytes != StepReportBudget/len(labels) || !ended.Truncated {
		t.Fatalf("事件应带每份报告的上限：%+v", ended.Info)
	}
}

func TestWorkflowTasksShareThePoolWithTask(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	children := newScriptedChildren(h)
	h.startWorkflow("一")
	labels := []string{"A", "B", "C", "D", "E"}
	if _, err := h.runStep(1, labels...); err != nil {
		t.Fatal(err)
	}
	if _, err := h.manager.Spawn(context.Background(), freshReadRequest("单个")); err == nil || !strings.Contains(err.Error(), "最多 5 个") {
		t.Fatalf("名额被工作流占满时 task 应被拒绝：%v", err)
	}
	for _, label := range labels {
		children.finish(label, "完成")
	}
	h.waitEnded(len(labels))
	if _, err := h.manager.Spawn(context.Background(), freshReadRequest("单个")); err != nil {
		t.Fatalf("名额空出后 task 应可派出：%v", err)
	}
	children.finish("单个", "完成")
	h.waitEnded(len(labels) + 1)
	if h.eventsOf(EventEnded)[len(labels)].Injected != true {
		t.Fatal("单个委派的结果仍应单独注入")
	}
}

func TestWorkflowSnapshotsCarryVersions(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	children := newScriptedChildren(h)
	h.startWorkflow("一")
	first, _ := h.manager.Workflow()
	second, _ := h.manager.Workflow()
	if first.Version == 0 || second.Version <= first.Version {
		t.Fatalf("每次快照的版本应递增，界面据此丢弃乱序的旧快照：%d %d", first.Version, second.Version)
	}
	if _, err := h.runStep(1, "A", "B"); err != nil {
		t.Fatal(err)
	}
	children.finish("A", "完成")
	children.finish("B", "完成")
	h.waitFor("工作流收尾", func() bool { return len(h.eventsOf(EventWorkflowEnded)) == 1 })
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := 0
	for _, event := range h.events {
		if event.Workflow != nil {
			seen++
			if event.Workflow.Version == 0 {
				t.Fatalf("工作流事件的快照应带版本：%s", event.Type)
			}
		}
	}
	if seen < 5 {
		t.Fatalf("工作流事件应带快照：%d", seen)
	}
}

func TestParentRunsWorkflowToolEndToEnd(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	var mu sync.Mutex
	parentCalls := 0
	var finalSaw string
	h.respond = func(_ context.Context, req model.Request) (model.Response, error) {
		if isChild(req) {
			return textResponse("子任务结论 " + taskLabel(requestTexts(req))), nil
		}
		mu.Lock()
		defer mu.Unlock()
		parentCalls++
		switch parentCalls {
		case 1:
			return callResponse("w1", "workflow", `{"action":"start","title":"演示","steps":[{"title":"调研","goal":"看一看"},{"title":"收尾","goal":"总结"}]}`), nil
		case 2:
			return callResponse("w2", "workflow", `{"action":"run","step":1,"tasks":[{"description":"甲","prompt":"任务：甲","kind":"fresh"},{"description":"乙","prompt":"任务：乙","kind":"fresh"}]}`), nil
		case 3:
			return callResponse("w3", "bash", `{"command":"sleep 0.3"}`), nil
		}
		finalSaw = strings.Join(requestTexts(req), "\n")
		return textResponse("汇总完成"), nil
	}
	result, err := h.parent.Run(context.Background(), "做个工作流", nil)
	if err != nil || result.Status != agent.StatusCompleted {
		t.Fatalf("主会话应在同一轮内完成：%+v %v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{"工作流「演示」已登记", "第 1 步「调研」已启动", "子任务结论 甲", "子任务结论 乙", "[工作流进度]", "下一步：第 2 步「收尾」"} {
		if !strings.Contains(finalSaw, want) {
			t.Fatalf("主 Agent 的后续请求缺少 %q：\n%s", want, finalSaw)
		}
	}
	if h.parent.InboxLen() != 0 || result.UserTurn != 1 {
		t.Fatalf("整步消息应在当轮注入且不产生新的用户轮次：inbox=%d turn=%d", h.parent.InboxLen(), result.UserTurn)
	}
}

func TestForkInsideWorkflowSeesSnapshotButCannotUseWorkflow(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	var mu sync.Mutex
	var denied string
	h.respond = func(_ context.Context, req model.Request) (model.Response, error) {
		if !isChild(req) {
			return textResponse("好"), nil
		}
		if result := req.Messages[len(req.Messages)-1].Content[0].ToolResult; result != nil {
			mu.Lock()
			denied = result.Content
			mu.Unlock()
			return textResponse("分身完成"), nil
		}
		return callResponse("w1", "workflow", `{"action":"end","reason":"x"}`), nil
	}
	h.runParent("私有背景")
	h.startWorkflow("一", "二")
	fork := tool.SpawnRequest{Description: "分身", Prompt: "任务：分身", Kind: tool.SpawnFork, Access: tool.SpawnRead}
	if _, err := h.manager.RunWorkflowStep(context.Background(), 1, []tool.SpawnRequest{fork}); err != nil {
		t.Fatal(err)
	}
	h.waitEnded(1)
	requests := h.childRequests()
	if !strings.Contains(strings.Join(requestTexts(requests[0]), "\n"), "私有背景") || !strings.Contains(toolNames(requests[0].Tools), "workflow") {
		t.Fatal("fork 任务应带着派出时的对话快照，并沿用主 Agent 的工具定义")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(denied, "不能使用工作流") {
		t.Fatalf("fork 任务调用 workflow 应被明确拒绝：%q", denied)
	}
	if !h.manager.WorkflowActive() {
		t.Fatal("子任务的拒绝调用不应影响工作流，第一步结束后它应仍在进行")
	}
}
