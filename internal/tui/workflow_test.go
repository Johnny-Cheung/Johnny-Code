package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
	"github.com/charmbracelet/x/ansi"
)

func sampleWorkflow(version uint64) *WorkflowNotice {
	started := time.Now().Add(-65 * time.Second)
	return &WorkflowNotice{Version: version, Title: "重构日志模块", Status: workflowActive, Started: started, Steps: []WorkflowStepNotice{
		{No: 1, Title: "调研现状", State: stepDone, Total: 2, Finished: 2, Completed: 2},
		{No: 2, Title: "并行改造各子模块", State: stepRunning, Total: 5, Finished: 3, Completed: 3, Active: []WorkflowTaskNotice{
			{ID: "a1b2", Description: "改造 parser", Started: started},
			{ID: "c3d4", Description: "改造 writer", Started: started, Waiting: true},
		}},
		{No: 3, Title: "集成测试", State: stepPending},
		{No: 4, Title: "汇总", State: stepPending},
	}}
}

func workflowModel(t *testing.T, width, height int) *terminalModel {
	t.Helper()
	m, _ := subagentModel(t)
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func sendWorkflow(m *terminalModel, w *WorkflowNotice) {
	m.Update(subagentMsg{notice: SubagentNotice{Kind: SubagentWorkflow, Workflow: w}})
}

func TestWorkflowAreaSitsBelowInputAndAboveFooter(t *testing.T) {
	m := workflowModel(t, 90, 30)
	sendWorkflow(m, sampleWorkflow(1))
	m.Update(subagentMsg{notice: startNotice("ef56", "查日志")})
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	var borders []int
	header, single, footer := -1, -1, -1
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "──"):
			borders = append(borders, i)
		case strings.HasPrefix(line, "workflow 重构日志模块"):
			header = i
		case strings.HasPrefix(line, "◆ ef56"):
			single = i
		case strings.Contains(line, "Enter 发送"):
			footer = i
		}
	}
	if len(borders) != 2 || header <= borders[1] || single <= header || footer <= single || footer != len(lines)-1 {
		t.Fatalf("任务区应在输入框下边线之下、页脚之上，workflow 在单个子任务之前：borders=%v header=%d single=%d footer=%d\n%s", borders, header, single, footer, strings.Join(lines, "\n"))
	}
	for _, want := range []string{"workflow 重构日志模块  2/4", "✓ 1 调研现状", "◆ 2 并行改造各子模块    3/5 个任务完成", "    ◆ a1b2 改造 parser", "    ◆ c3d4 改造 writer", "等待审批", "○ 3 集成测试", "○ 4 汇总"} {
		if !strings.Contains(strings.Join(lines, "\n"), want) {
			t.Fatalf("任务区缺少 %q：\n%s", want, strings.Join(lines, "\n"))
		}
	}
}

func TestCursorIgnoresTaskAreaBelowInput(t *testing.T) {
	m := workflowModel(t, 60, 30)
	m.Update(tea.PasteMsg{Content: "你好"})
	before := m.View().Cursor
	sendWorkflow(m, sampleWorkflow(1))
	m.Update(subagentMsg{notice: startNotice("ef56", "查日志")})
	after := m.View().Cursor
	if before == nil || after == nil || before.Y != after.Y || before.X != after.X {
		t.Fatalf("任务区在输入框之下，不应改变光标位置：%+v %+v", before, after)
	}
}

func TestBusyViewKeepsTaskAreaBetweenProgressAndStatus(t *testing.T) {
	m := workflowModel(t, 90, 30)
	sendWorkflow(m, sampleWorkflow(1))
	m.busy = true
	m.status = "执行中"
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	header, status, hint := -1, -1, -1
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "workflow "):
			header = i
		case line == "执行中":
			status = i
		case strings.Contains(line, "Ctrl+C 取消本轮和子任务"):
			hint = i
		}
	}
	if header < 0 || status <= header || hint <= status {
		t.Fatalf("忙碌时任务区应在状态行之前，提示应说明取消子任务：%d %d %d\n%s", header, status, hint, strings.Join(lines, "\n"))
	}
}

func TestTaskAreaFoldsByPriorityWithinOneThirdOfHeight(t *testing.T) {
	w := &WorkflowNotice{Version: 1, Title: "大工作流", Status: workflowActive, Started: time.Now()}
	for i := 1; i <= 10; i++ {
		step := WorkflowStepNotice{No: i, Title: "步骤", State: stepPending}
		switch {
		case i <= 3:
			step.State = stepDone
		case i == 4:
			step.State, step.Total, step.Finished = stepRunning, 5, 0
			for j := 0; j < 5; j++ {
				step.Active = append(step.Active, WorkflowTaskNotice{ID: "t" + string(rune('a'+j)), Description: "任务", Started: time.Now()})
			}
		}
		w.Steps = append(w.Steps, step)
	}
	m := workflowModel(t, 90, 24)
	sendWorkflow(m, w)
	lines := m.taskAreaLines(90, m.areaHeightLimit(20))
	if len(lines) > 8 {
		t.Fatalf("任务区最多占终端高度的三分之一：%d 行\n%s", len(lines), strings.Join(lines, "\n"))
	}
	text := strings.Join(lines, "\n")
	for _, want := range []string{"workflow 大工作流  4/10", "◆ 4 步骤", "另有"} {
		if !strings.Contains(text, want) {
			t.Fatalf("折叠后应保留标题和当前步骤并说明省略：%q\n%s", want, text)
		}
	}
	if strings.Contains(text, "✓ 1 步骤") {
		t.Fatalf("已完成的步骤应被折叠或丢弃：\n%s", text)
	}

	short := workflowModel(t, 90, 6)
	sendWorkflow(short, w)
	summary := short.taskAreaLines(90, short.areaHeightLimit(5))
	if len(summary) != 1 || summary[0] != "workflow 4/10 · 0/5 个任务" {
		t.Fatalf("终端太矮时只显示一行摘要：%q", summary)
	}
}

func TestWorkflowSnapshotsAreOrderedAndEndedWorkflowStaysGone(t *testing.T) {
	m := workflowModel(t, 90, 30)
	sendWorkflow(m, sampleWorkflow(5))
	sendWorkflow(m, sampleWorkflow(3))
	if m.workflow == nil || m.workflow.Version != 5 {
		t.Fatal("乱序到达的旧快照应被丢弃")
	}
	end := sampleWorkflow(6)
	end.Status = workflowCompleted
	m.Update(subagentMsg{notice: SubagentNotice{Kind: SubagentWorkflowEnd, Workflow: end}})
	if m.workflow != nil {
		t.Fatal("工作流结束后应从任务区移除")
	}
	sendWorkflow(m, sampleWorkflow(4))
	if m.workflow != nil {
		t.Fatal("迟到的旧快照不能让已结束的工作流重新出现")
	}
	sendWorkflow(m, sampleWorkflow(7))
	if m.workflow == nil {
		t.Fatal("新的工作流应能显示")
	}
}

func TestWorkflowTasksStayOutOfSingleTaskRowsAndPrintNothing(t *testing.T) {
	m := workflowModel(t, 90, 30)
	sendWorkflow(m, sampleWorkflow(1))
	step := SubagentNotice{Kind: SubagentStart, ID: "a1b2", Description: "改造 parser", Started: time.Now(), Step: 2}
	m.Update(subagentMsg{notice: step})
	if len(m.subs) != 0 {
		t.Fatal("工作流的任务不应进入单个子任务行")
	}
	step.Kind, step.Status = SubagentEnd, agent.StatusCompleted
	if _, cmd := m.Update(subagentMsg{notice: step}); cmd != nil {
		t.Fatal("工作流内的任务结束时不应逐个打印提示")
	}
	m.applySubagent(startNotice("ab12", "单独"))
	if len(m.subs) != 1 {
		t.Fatal("单个委派的子任务仍应进入子任务行")
	}
	end := startNotice("ab12", "单独")
	end.Kind, end.Status = SubagentEnd, agent.StatusCompleted
	if cmd := m.applySubagent(end); cmd == nil || len(m.subs) != 0 {
		t.Fatal("单个委派的子任务结束时仍应打印提示并从行里移除")
	}
}

func TestStepAndWorkflowEndLines(t *testing.T) {
	w := sampleWorkflow(1)
	w.Steps[1].State, w.Steps[1].Failed, w.Steps[1].Completed, w.Steps[1].Total = stepDone, 1, 4, 5
	line := stepEndLine(w, 2)
	for _, want := range []string{"△", "第 2/4 步", "并行改造各子模块", "5 个任务", "4 个完成", "1 个失败"} {
		if !strings.Contains(line, want) {
			t.Fatalf("步骤结束提示缺少 %q：%s", want, line)
		}
	}
	if clean := stepEndLine(sampleWorkflow(1), 1); !strings.Contains(clean, "✓") || !strings.Contains(clean, "全部完成") {
		t.Fatalf("全部完成时应标 ✓：%s", clean)
	}
	cases := map[string][]string{
		workflowCompleted: {"✓", "已完成", "4 步全部结束"},
		workflowEnded:     {"○", "已提前结束", "完成 1/4 步", "原因：方向不对"},
		workflowCanceled:  {"○", "已取消", "完成 1/4 步"},
	}
	for status, wants := range cases {
		w := sampleWorkflow(1)
		w.Status, w.Reason = status, "方向不对"
		if status == workflowCompleted {
			for i := range w.Steps {
				w.Steps[i].State = stepDone
			}
		}
		line := workflowEndLine(w)
		for _, want := range wants {
			if !strings.Contains(line, want) {
				t.Fatalf("%s 的结束提示缺少 %q：%s", status, want, line)
			}
		}
		if strings.Contains(line, "\n") {
			t.Fatalf("结束提示应只有一行：%q", line)
		}
	}
}

func TestStepWakesUseTheirOwnLimitAndResetOnUserInput(t *testing.T) {
	m, control := subagentModel(t)
	rounds := 0
	m.options.RunWake = func(ctx context.Context, emit func(Event) error, notice func(string)) (agent.Result, error) {
		control.mu.Lock()
		control.pending = 0
		rounds++
		control.mu.Unlock()
		return agent.Result{}, nil
	}
	finish := func() {
		select {
		case item := <-m.events:
			m.Update(item)
		case <-time.After(time.Second):
			t.Fatal("轮次没有结束")
		}
	}
	sendWorkflow(m, sampleWorkflow(1))
	for i := 0; i < maxWakeRounds+3; i++ {
		control.mu.Lock()
		control.pending = 1
		control.mu.Unlock()
		m.Update(subagentMsg{notice: SubagentNotice{Kind: SubagentStepEnd, Step: 1, Workflow: sampleWorkflow(uint64(10 + i))}})
		if !m.busy || m.wakeBlocked {
			t.Fatalf("工作流期间第 %d 次步骤唤醒不应被普通上限拦住：busy=%v blocked=%v", i+1, m.busy, m.wakeBlocked)
		}
		finish()
	}
	if m.stepWakes != maxWakeRounds+3 || m.wakeRounds != 0 {
		t.Fatalf("步骤唤醒应单独计数：stepWakes=%d wakeRounds=%d", m.stepWakes, m.wakeRounds)
	}
}

func TestWorkflowWakesEventuallyHitTheirLimitAcrossWorkflows(t *testing.T) {
	m, control := subagentModel(t)
	m.options.RunTurn = func(context.Context, string, func(Event) error, func(string)) (agent.Result, error) {
		return agent.Result{}, nil
	}
	m.options.RunWake = func(ctx context.Context, emit func(Event) error, notice func(string)) (agent.Result, error) {
		control.mu.Lock()
		control.pending = 0
		control.mu.Unlock()
		return agent.Result{}, nil
	}
	finish := func() {
		select {
		case item := <-m.events:
			m.Update(item)
		case <-time.After(time.Second):
			t.Fatal("轮次没有结束")
		}
	}
	for i := 0; i < maxStepWakes; i++ {
		version := uint64(100 + i)
		sendWorkflow(m, sampleWorkflow(version))
		control.mu.Lock()
		control.pending = 1
		control.mu.Unlock()
		m.Update(subagentMsg{notice: SubagentNotice{Kind: SubagentStepEnd, Step: 1, Workflow: sampleWorkflow(version)}})
		if !m.busy {
			t.Fatalf("第 %d 次唤醒应发生", i+1)
		}
		end := sampleWorkflow(version + 1)
		end.Status = workflowCompleted
		m.Update(subagentMsg{notice: SubagentNotice{Kind: SubagentWorkflowEnd, Workflow: end}})
		finish()
	}
	sendWorkflow(m, sampleWorkflow(999))
	control.mu.Lock()
	control.pending = 1
	control.mu.Unlock()
	if _, cmd := m.Update(subagentMsg{notice: SubagentNotice{Kind: SubagentStepEnd, Step: 1, Workflow: sampleWorkflow(999)}}); m.busy || !m.wakeBlocked || cmd == nil {
		t.Fatalf("连续工作流的步骤唤醒达到上限后应停止：busy=%v blocked=%v", m.busy, m.wakeBlocked)
	}
	m.input.SetValue("继续")
	m.Update(keyPress(tea.KeyEnter))
	if m.stepWakes != 0 || m.wakeBlocked || !m.busy {
		t.Fatalf("用户输入应重置步骤唤醒计数：stepWakes=%d blocked=%v", m.stepWakes, m.wakeBlocked)
	}
}

func TestPlainWakesStayLimitedWhileWorkflowIsShown(t *testing.T) {
	m, control := subagentModel(t)
	m.options.RunWake = func(ctx context.Context, emit func(Event) error, notice func(string)) (agent.Result, error) {
		control.mu.Lock()
		control.pending = 0
		control.mu.Unlock()
		return agent.Result{}, nil
	}
	finish := func() {
		select {
		case item := <-m.events:
			m.Update(item)
		case <-time.After(time.Second):
			t.Fatal("轮次没有结束")
		}
	}
	sendWorkflow(m, sampleWorkflow(1))
	for i := 0; i < maxWakeRounds; i++ {
		control.mu.Lock()
		control.pending = 1
		control.mu.Unlock()
		m.Update(subagentMsg{notice: SubagentNotice{Kind: SubagentEnd, ID: "ab12", Status: agent.StatusCompleted}})
		if !m.busy {
			t.Fatalf("第 %d 次普通唤醒应发生", i+1)
		}
		finish()
	}
	control.mu.Lock()
	control.pending = 1
	control.mu.Unlock()
	m.Update(subagentMsg{notice: SubagentNotice{Kind: SubagentEnd, ID: "cd34", Status: agent.StatusCompleted}})
	if m.busy || !m.wakeBlocked {
		t.Fatalf("工作流显示期间普通唤醒仍只允许 %d 次：busy=%v blocked=%v", maxWakeRounds, m.busy, m.wakeBlocked)
	}
}

func TestApprovalHintNamesWorkflowCancellation(t *testing.T) {
	cases := []struct {
		busy     bool
		workflow bool
		subs     int
		want     string
	}{
		{busy: true, workflow: true, want: "Ctrl+C 取消本轮和整个工作流"},
		{busy: true, subs: 1, want: "Ctrl+C 取消本轮和子任务"},
		{busy: true, want: "Ctrl+C 取消本轮"},
		{workflow: true, want: "Ctrl+C 取消整个工作流"},
		{want: "Ctrl+C 取消全部子任务"},
	}
	for _, item := range cases {
		m, _ := subagentModel(t)
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		m.busy = item.busy
		if item.workflow {
			sendWorkflow(m, sampleWorkflow(1))
		}
		for i := 0; i < item.subs; i++ {
			m.applySubagent(startNotice("ab12", "查日志"))
		}
		m.beginApproval(&approvalDelivery{ctx: context.Background(), request: tool.ApprovalRequest{Tool: "bash", Target: "x", Origin: tool.Origin{ID: "ab12"}}, response: make(chan tool.ApprovalDecision, 1)})
		if view := m.View().Content; !strings.Contains(view, item.want) {
			t.Fatalf("审批面板应按实际动作提示 %q：%q", item.want, view)
		}
	}
}

func TestLongTitlesAreTruncatedBeforeStatus(t *testing.T) {
	w := &WorkflowNotice{Version: 1, Title: strings.Repeat("很长的标题", 12), Status: workflowActive, Started: time.Now(), Steps: []WorkflowStepNotice{
		{No: 1, Title: strings.Repeat("步骤名", 20), State: stepRunning, Total: 5, Finished: 3,
			Active: []WorkflowTaskNotice{{ID: "a1b2", Description: strings.Repeat("描述", 30), Started: time.Now(), Waiting: true}}},
	}}
	m := workflowModel(t, 60, 30)
	sendWorkflow(m, w)
	lines := m.taskAreaLines(60, 10)
	text := strings.Join(lines, "\n")
	for _, want := range []string{"1/1", "3/5 个任务完成", "等待审批"} {
		if !strings.Contains(text, want) {
			t.Fatalf("窄终端下应保留行尾状态 %q：\n%s", want, text)
		}
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 60 {
			t.Fatalf("行宽超出终端：%q", line)
		}
	}
}
