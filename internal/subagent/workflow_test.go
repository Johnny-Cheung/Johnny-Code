package subagent

import (
	"context"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func TestStepBarrierDeliversOneMergedReportAndProgress(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	children := newScriptedChildren(h)
	h.startWorkflow("调研", "实现")
	if _, err := h.runStep(1, "A", "B", "C"); err != nil {
		t.Fatal(err)
	}
	h.waitFor("三个任务启动", func() bool { return len(h.eventsOf(EventStarted)) == 3 })
	children.finish("A", "结论A")
	children.finish("B", "结论B")
	h.waitEnded(2)
	if h.parent.InboxLen() != 0 || len(h.eventsOf(EventStepEnded)) != 0 {
		t.Fatal("步骤没有全部结束时不应送回任何报告")
	}
	children.finish("C", "结论C")
	h.waitEnded(3)
	if h.parent.InboxLen() != 1 {
		t.Fatalf("整步只应送回一条消息：%d", h.parent.InboxLen())
	}
	message, sources := h.deliver()
	for _, want := range []string{"第 1/2 步「调研」已结束：3 个任务，全部完成", "结论A", "结论B", "结论C", "仅作数据", "[工作流进度]", "✓ 1 调研", "○ 2 实现", "下一步：第 2 步「实现」", "step=2"} {
		if !strings.Contains(message, want) {
			t.Fatalf("整步消息缺少 %q：\n%s", want, message)
		}
	}
	if strings.Index(message, "结论A") > strings.Index(message, "结论B") || strings.Index(message, "结论B") > strings.Index(message, "结论C") {
		t.Fatal("报告应按任务在这一步里的顺序排列")
	}
	if len(sources) != 1 || sources[0] != "workflow:step-1" {
		t.Fatalf("消息来源应标明工作流步骤：%v", sources)
	}
	for _, ended := range h.eventsOf(EventEnded) {
		if ended.Injected || ended.Info.Step != 1 {
			t.Fatalf("工作流任务不单独注入，并标明所属步骤：%+v", ended)
		}
	}
	if len(h.eventsOf(EventStepStarted)) != 1 || len(h.eventsOf(EventStepEnded)) != 1 || len(h.eventsOf(EventWorkflowEnded)) != 0 {
		t.Fatal("应有一次步骤启动和一次步骤结束事件，工作流尚未结束")
	}

	if _, err := h.runStep(2, "D"); err != nil {
		t.Fatal(err)
	}
	children.finish("D", "结论D")
	h.waitEnded(4)
	h.waitFor("工作流收尾", func() bool { return len(h.eventsOf(EventWorkflowEnded)) == 1 })
	last, _ := h.deliver()
	if !strings.Contains(last, "所有 2 步都已结束，工作流完成") || strings.Contains(last, "下一步") {
		t.Fatalf("最后一步的消息应说明工作流完成：\n%s", last)
	}
	end := h.eventsOf(EventWorkflowEnded)[0]
	if end.Workflow == nil || end.Workflow.Status != WorkflowCompleted || end.Workflow.Done() != 2 {
		t.Fatalf("结束事件应带最终状态：%+v", end.Workflow)
	}
	if _, err := h.runStep(2, "E"); err == nil || !strings.Contains(err.Error(), "已经全部完成") {
		t.Fatalf("完成后不应再启动步骤：%v", err)
	}
	if h.manager.WorkflowActive() {
		t.Fatal("完成后不应还有进行中的工作流")
	}
}

func TestStepOrderUniquenessAndOneWorkflowAtATime(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	children := newScriptedChildren(h)
	if _, err := h.runStep(1, "A"); err == nil || !strings.Contains(err.Error(), "还没有登记工作流") {
		t.Fatalf("没有工作流时 run 应被拒绝：%v", err)
	}
	h.startWorkflow("一", "二", "三")
	if _, err := h.manager.StartWorkflow("另一个", outline("甲")); err == nil || !strings.Contains(err.Error(), "已有进行中的工作流") {
		t.Fatalf("同一时间只允许一个工作流：%v", err)
	}
	if _, err := h.runStep(2, "A"); err == nil || !strings.Contains(err.Error(), "按顺序启动，下一步是第 1 步") {
		t.Fatalf("跳步应被拒绝：%v", err)
	}
	if _, err := h.runStep(4, "A"); err == nil || !strings.Contains(err.Error(), "无效") {
		t.Fatalf("超出大纲的步骤应被拒绝：%v", err)
	}
	if _, err := h.runStep(1, "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.runStep(1, "B"); err == nil || !strings.Contains(err.Error(), "还没有结束") {
		t.Fatalf("进行中的步骤不能重复启动：%v", err)
	}
	if _, err := h.runStep(2, "B"); err == nil || !strings.Contains(err.Error(), "还没有结束") {
		t.Fatalf("上一步没结束不能启动下一步：%v", err)
	}
	children.finish("A", "完成")
	h.waitEnded(1)
	if _, err := h.runStep(1, "B"); err == nil || !strings.Contains(err.Error(), "已经完成，不能重复启动") {
		t.Fatalf("完成的步骤不能重复启动：%v", err)
	}
	if _, err := h.runStep(2, "B"); err != nil {
		t.Fatalf("上一步结束后应能启动下一步：%v", err)
	}
	children.finish("B", "完成")
	h.waitEnded(2)
}

func TestRunStepRejectsWholeBatchAndAllowsRetry(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	children := newScriptedChildren(h)
	h.startWorkflow("唯一的一步")
	write := func(label, path string) tool.SpawnRequest {
		return tool.SpawnRequest{Description: label, Prompt: "任务：" + label, Kind: tool.SpawnFresh, Access: tool.SpawnWrite, Paths: []string{h.path(path)}}
	}
	overlapping := []tool.SpawnRequest{freshReadRequest("R"), write("W1", "pkg"), write("W2", "pkg/sub")}
	if _, err := h.manager.RunWorkflowStep(context.Background(), 1, overlapping); err == nil {
		t.Fatal("范围重叠应整批拒绝")
	}
	tooMany := make([]tool.SpawnRequest, tool.MaxWorkflowTasks+1)
	for i := range tooMany {
		tooMany[i] = freshReadRequest("X")
	}
	if _, err := h.manager.RunWorkflowStep(context.Background(), 1, tooMany); err == nil {
		t.Fatal("超过每步上限应整批拒绝")
	}
	if _, err := h.manager.Spawn(context.Background(), write("占用", "docs")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.manager.RunWorkflowStep(context.Background(), 1, []tool.SpawnRequest{write("W3", "docs/a")}); err == nil {
		t.Fatal("与运行中任务的范围冲突应整批拒绝")
	}
	if h.manager.Running() != 1 || len(h.manager.claims.Owners()) != 1 {
		t.Fatal("被拒绝的整批不应启动任务，也不应留下范围")
	}
	h.manager.mu.Lock()
	state := h.manager.wf.steps[0].state
	h.manager.mu.Unlock()
	if state != StepPending {
		t.Fatalf("被拒绝后这一步仍应是待启动：%s", state)
	}
	if _, err := h.runStep(1, "R"); err != nil {
		t.Fatalf("修正后应能重新启动：%v", err)
	}
	children.finish("R", "完成")
	children.finish("占用", "完成")
	h.waitEnded(2)
}

func TestFailedTaskDoesNotEndWorkflow(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	children := newScriptedChildren(h)
	h.startWorkflow("一", "二")
	if _, err := h.runStep(1, "好", "坏"); err != nil {
		t.Fatal(err)
	}
	children.finish("好", "没问题")
	children.finish("坏", "ERROR:服务不可用")
	h.waitEnded(2)
	message, _ := h.deliver()
	for _, want := range []string{"1 个完成，1 个失败", "服务不可用", "没有正常结束", "没问题", "下一步：第 2 步"} {
		if !strings.Contains(message, want) {
			t.Fatalf("失败的任务应照常写进整步消息 %q：\n%s", want, message)
		}
	}
	if !h.manager.WorkflowActive() {
		t.Fatal("有任务失败不应终止工作流")
	}
}
