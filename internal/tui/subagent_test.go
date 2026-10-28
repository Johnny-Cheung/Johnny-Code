package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

type fakeSubagents struct {
	mu        sync.Mutex
	running   int
	pending   int
	cancelled int
	waited    bool
	listener  func(SubagentNotice)
}

func (f *fakeSubagents) Running() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

func (f *fakeSubagents) CancelAll() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled++
	return f.running
}

func (f *fakeSubagents) Wait() {
	f.mu.Lock()
	f.waited = true
	f.mu.Unlock()
}

func (f *fakeSubagents) PendingResults() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pending
}

func (f *fakeSubagents) Subscribe(listener func(SubagentNotice)) func() {
	f.mu.Lock()
	f.listener = listener
	f.mu.Unlock()
	return func() {}
}

func subagentModel(t *testing.T) (*terminalModel, *fakeSubagents) {
	t.Helper()
	m := testModel(t)
	control := &fakeSubagents{}
	m.options.Subagents = control
	return m, control
}

func startNotice(id, description string) SubagentNotice {
	return SubagentNotice{Kind: SubagentStart, ID: id, Description: description, Started: time.Now().Add(-65 * time.Second)}
}

func TestSubagentAreaShowsRunningTasksAndWaitingState(t *testing.T) {
	m, _ := subagentModel(t)
	m.Update(subagentMsg{notice: startNotice("ab12", "查日志")})
	m.Update(subagentMsg{notice: startNotice("cd34", "写文档")})
	view := m.View().Content
	for _, want := range []string{"ab12 查日志", "cd34 写文档", "1m05s", "运行中"} {
		if !strings.Contains(view, want) {
			t.Fatalf("空闲视图缺少子任务区内容 %q：%q", want, view)
		}
	}
	m.Update(subagentMsg{notice: SubagentNotice{Kind: SubagentState, ID: "ab12", Waiting: true}})
	if view := m.View().Content; !strings.Contains(view, "等待审批") {
		t.Fatalf("应显示等待审批：%q", view)
	}
	m.busy = true
	if view := m.View().Content; !strings.Contains(view, "ab12 查日志") || !strings.Contains(view, "Ctrl+C 取消本轮和子任务") {
		t.Fatalf("忙碌视图也应显示子任务区：%q", view)
	}
	m.busy = false
	m.Update(subagentMsg{notice: SubagentNotice{Kind: SubagentEnd, ID: "ab12", Status: agent.StatusCompleted, Duration: time.Second}})
	if view := m.View().Content; strings.Contains(view, "ab12") || !strings.Contains(view, "cd34") {
		t.Fatalf("结束的子任务应从常驻区移除：%q", view)
	}
}

func TestSubagentEndLinesCoverEveryOutcome(t *testing.T) {
	cases := []struct {
		notice SubagentNotice
		want   []string
	}{
		{SubagentNotice{ID: "a1", Description: "查日志", Status: agent.StatusCompleted, Duration: 12 * time.Second}, []string{"✓", "a1", "查日志", "已完成", "12s"}},
		{SubagentNotice{ID: "a2", Description: "写文档", Status: agent.StatusCanceled}, []string{"○", "已取消"}},
		{SubagentNotice{ID: "a3", Description: "长任务", Status: agent.StatusMaxTurns, Partial: true}, []string{"△", "只完成了一部分", "请求次数上限"}},
		{SubagentNotice{ID: "a4", Description: "失败", Status: agent.StatusFailed, Error: "第 1 次模型请求失败：服务不可用"}, []string{"✗", "失败", "服务不可用"}},
	}
	for _, item := range cases {
		line := subagentEndLine(item.notice)
		for _, want := range item.want {
			if !strings.Contains(line, want) {
				t.Fatalf("结束提示缺少 %q：%s", want, line)
			}
		}
		if strings.Contains(line, "\n") {
			t.Fatalf("结束提示应只有一行：%q", line)
		}
	}
	long := subagentEndLine(SubagentNotice{ID: "a5", Status: agent.StatusFailed, Error: strings.Repeat("错", 300)})
	if len([]rune(long)) > 200 {
		t.Fatalf("失败原因应被截断：%d", len([]rune(long)))
	}
}

func TestApprovalQueueIsFIFOAndWorksWhileIdle(t *testing.T) {
	m, _ := subagentModel(t)
	first, second := make(chan tool.ApprovalDecision, 1), make(chan tool.ApprovalDecision, 1)
	m.beginApproval(&approvalDelivery{ctx: context.Background(), request: tool.ApprovalRequest{Tool: "bash", Target: "mkdir a", Reason: "需要审批", Origin: tool.Origin{ID: "ab12", Description: "建目录"}}, response: first})
	m.beginApproval(&approvalDelivery{ctx: context.Background(), request: tool.ApprovalRequest{Tool: "write", Target: "/x/y", Reason: "范围外"}, response: second})
	if m.busy {
		t.Fatal("本测试针对主 Agent 空闲的场景")
	}
	view := m.View().Content
	for _, want := range []string{"来源：子任务 ab12 建目录", "mkdir a", "另有 1 个请求在等待"} {
		if !strings.Contains(view, want) {
			t.Fatalf("空闲时也应显示队首审批 %q：%q", want, view)
		}
	}
	m.Update(keyPress(tea.KeyUp))
	m.Update(keyPress(tea.KeyEnter))
	if decision := <-first; !decision.Allow {
		t.Fatalf("应允许第一个请求：%#v", decision)
	}
	if m.approval == nil || m.approval.request.Target != "/x/y" || len(m.approvalQueue) != 0 {
		t.Fatalf("第一个处理后应轮到第二个：%+v", m.approval)
	}
	if view := m.View().Content; strings.Contains(view, "来源：") || strings.Contains(view, "另有") {
		t.Fatalf("主 Agent 的请求不带来源，队列已空：%q", view)
	}
	m.Update(keyPress(tea.KeyEnter))
	if decision := <-second; decision.Allow {
		t.Fatalf("默认焦点是拒绝：%#v", decision)
	}
	if m.approval != nil || m.status != "空闲" {
		t.Fatalf("处理完毕后应恢复空闲：%v %q", m.approval, m.status)
	}
}

func TestStaleApprovalsAreDroppedAndTurnEndKeepsLiveOnes(t *testing.T) {
	m, _ := subagentModel(t)
	deadCtx, cancel := context.WithCancel(context.Background())
	m.beginApproval(&approvalDelivery{ctx: deadCtx, request: tool.ApprovalRequest{Tool: "bash", Target: "dead", Origin: tool.Origin{ID: "aa11"}}, response: make(chan tool.ApprovalDecision, 1)})
	m.beginApproval(&approvalDelivery{ctx: context.Background(), request: tool.ApprovalRequest{Tool: "bash", Target: "alive", Origin: tool.Origin{ID: "bb22"}}, response: make(chan tool.ApprovalDecision, 1)})
	cancel()
	m.Update(subagentMsg{notice: SubagentNotice{Kind: SubagentEnd, ID: "aa11", Status: agent.StatusCanceled}})
	if m.approval == nil || m.approval.request.Target != "alive" {
		t.Fatalf("已取消子任务的审批应被丢弃：%+v", m.approval)
	}
	m.busy = true
	m.cancel = func() {}
	m.Update(delivery{done: true})
	if m.approval == nil || m.approval.request.Target != "alive" {
		t.Fatal("主 Agent 一轮结束不应清空其他子任务的审批")
	}
}

func TestIdleApprovalFromSubagentReachesGateWithoutBusyTurn(t *testing.T) {
	m, _ := subagentModel(t)
	gate := tool.NewApprovalGate()
	gate.Set(m.requestApproval)
	decisions := make(chan tool.ApprovalDecision, 1)
	go func() {
		decision, _ := gate.Ask(context.Background(), tool.ApprovalRequest{Tool: "bash", Target: "mkdir x", Origin: tool.Origin{ID: "ab12", Description: "建目录"}})
		decisions <- decision
	}()
	select {
	case item := <-m.events:
		m.Update(item)
	case <-time.After(time.Second):
		t.Fatal("空闲时子任务审批应到达 TUI")
	}
	m.Update(keyPress(tea.KeyUp))
	m.Update(keyPress(tea.KeyEnter))
	select {
	case decision := <-decisions:
		if !decision.Allow {
			t.Fatalf("应回传允许：%#v", decision)
		}
	case <-time.After(time.Second):
		t.Fatal("决策未回传")
	}
}

func TestCtrlCCancelsSubagentsOnlyWhenInputIsEmpty(t *testing.T) {
	m, control := subagentModel(t)
	control.running = 2
	m.input.SetValue("草稿")
	m.Update(ctrlPress('c'))
	if m.input.Value() != "" || control.cancelled != 0 {
		t.Fatalf("输入框有内容时 Ctrl+C 只清空输入：%q %d", m.input.Value(), control.cancelled)
	}
	m.Update(ctrlPress('c'))
	if control.cancelled != 1 {
		t.Fatalf("输入框为空且有子任务时 Ctrl+C 应取消全部子任务：%d", control.cancelled)
	}
	control.running = 0
	m.Update(ctrlPress('c'))
	if control.cancelled != 2 || m.busy || m.quitting {
		t.Fatalf("没有子任务时 Ctrl+C 不应有其他副作用：%d", control.cancelled)
	}
}

func TestBusyCtrlCCancelsTurnAndSubagentsTogether(t *testing.T) {
	m, control := subagentModel(t)
	turnCtx, cancel := context.WithCancel(m.ctx)
	m.busy, m.cancel = true, cancel
	m.Update(ctrlPress('c'))
	if turnCtx.Err() == nil || control.cancelled != 1 || !m.stopping {
		t.Fatalf("忙碌时 Ctrl+C 应同时取消本轮和子任务：ctx=%v cancelled=%d", turnCtx.Err(), control.cancelled)
	}
	m.Update(ctrlPress('c'))
	if control.cancelled != 1 {
		t.Fatal("取消进行中不应重复取消")
	}
}

func TestApprovalCtrlCDropsQueueAndCancelsEverything(t *testing.T) {
	m, control := subagentModel(t)
	m.beginApproval(&approvalDelivery{ctx: context.Background(), request: tool.ApprovalRequest{Tool: "bash", Target: "a", Origin: tool.Origin{ID: "ab12"}}, response: make(chan tool.ApprovalDecision, 1)})
	m.beginApproval(&approvalDelivery{ctx: context.Background(), request: tool.ApprovalRequest{Tool: "bash", Target: "b", Origin: tool.Origin{ID: "cd34"}}, response: make(chan tool.ApprovalDecision, 1)})
	m.Update(ctrlPress('c'))
	if m.approval != nil || len(m.approvalQueue) != 0 || control.cancelled != 1 {
		t.Fatalf("Ctrl+C 应丢弃全部待审批并取消子任务：%v %d %d", m.approval, len(m.approvalQueue), control.cancelled)
	}
}

func TestExitWithRunningSubagentsCancelsAndWaitsBeforeQuit(t *testing.T) {
	for _, trigger := range []string{"/exit", "ctrl+d"} {
		m, control := subagentModel(t)
		control.running = 2
		var cmd tea.Cmd
		if trigger == "/exit" {
			m.input.SetValue("/exit")
			_, cmd = m.Update(keyPress(tea.KeyEnter))
		} else {
			_, cmd = m.Update(ctrlPress('d'))
		}
		if !m.quitting || cmd == nil {
			t.Fatalf("%s 应进入退出流程", trigger)
		}
		if control.cancelled != 0 || control.waited {
			t.Fatal("取消和等待应在命令中执行，而不是阻塞 Update")
		}
	}
	_, control := subagentModel(t)
	control.running = 1
	if _, ok := stopSubagentsAndQuit(control)().(tea.QuitMsg); !ok || control.cancelled != 1 || !control.waited {
		t.Fatalf("退出命令应先取消并等待子任务，再发出退出消息：cancelled=%d waited=%v", control.cancelled, control.waited)
	}
}

func TestExitWithoutSubagentsQuitsImmediately(t *testing.T) {
	m, _ := subagentModel(t)
	if _, ok := m.exit()().(tea.QuitMsg); !ok || !m.quitting {
		t.Fatal("没有子任务时应直接退出")
	}
}

func TestWakeStartsOnlyWhenIdleInputEmptyAndResultsPending(t *testing.T) {
	m, control := subagentModel(t)
	woke := make(chan struct{}, 8)
	m.options.RunWake = func(ctx context.Context, emit func(Event) error, notice func(string)) (agent.Result, error) {
		woke <- struct{}{}
		return agent.Result{}, nil
	}
	m.input.SetValue("草稿")
	control.pending = 1
	m.Update(subagentMsg{notice: SubagentNotice{Kind: SubagentEnd, ID: "ab12", Status: agent.StatusCompleted}})
	if m.busy {
		t.Fatal("用户正在输入时不应抢占")
	}
	m.input.Reset()
	m.Update(subagentMsg{notice: SubagentNotice{Kind: SubagentEnd, ID: "none", Status: agent.StatusCompleted}})
	if !m.busy || !strings.Contains(m.status, "子任务完成，主 Agent 正在汇总") {
		t.Fatalf("空闲且输入为空时应唤醒：busy=%v status=%q", m.busy, m.status)
	}
	select {
	case <-woke:
	case <-time.After(time.Second):
		t.Fatal("唤醒轮没有执行")
	}
	if len(m.history) != 0 {
		t.Fatalf("唤醒不应当作用户输入写入历史：%v", m.history)
	}
}

func TestWakeSafetyValveStopsAfterFiveRoundsUntilUserInput(t *testing.T) {
	m, control := subagentModel(t)
	control.pending = 1
	rounds := 0
	m.options.RunWake = func(ctx context.Context, emit func(Event) error, notice func(string)) (agent.Result, error) {
		rounds++
		return agent.Result{}, nil
	}
	m.options.RunTurn = func(ctx context.Context, prompt string, emit func(Event) error, notice func(string)) (agent.Result, error) {
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
	for i := 0; i < maxWakeRounds; i++ {
		m.Update(subagentMsg{notice: SubagentNotice{Kind: SubagentEnd, ID: "x"}})
		if !m.busy {
			t.Fatalf("第 %d 轮应唤醒", i+1)
		}
		finish()
	}
	if m.busy || !m.wakeBlocked || rounds != maxWakeRounds {
		t.Fatalf("连续唤醒达到上限后应停止：busy=%v blocked=%v rounds=%d", m.busy, m.wakeBlocked, rounds)
	}
	m.Update(subagentMsg{notice: SubagentNotice{Kind: SubagentEnd, ID: "y"}})
	if m.busy {
		t.Fatal("熔断后不应再唤醒")
	}
	m.input.SetValue("继续")
	m.Update(keyPress(tea.KeyEnter))
	if !m.busy || m.wakeBlocked || m.wakeRounds != 0 {
		t.Fatalf("用户输入应重置唤醒计数：busy=%v blocked=%v rounds=%d", m.busy, m.wakeBlocked, m.wakeRounds)
	}
}

func TestCanceledTurnDoesNotImmediatelyWakeAgain(t *testing.T) {
	m, control := subagentModel(t)
	control.pending = 1
	m.options.RunWake = func(context.Context, func(Event) error, func(string)) (agent.Result, error) {
		t.Error("用户取消本轮后不应立刻自动唤醒")
		return agent.Result{}, nil
	}
	m.busy, m.cancel = true, func() {}
	m.Update(delivery{done: true, err: context.Canceled})
	if m.busy || !m.wakeBlocked {
		t.Fatalf("取消后应回到空闲并暂停自动唤醒：busy=%v blocked=%v", m.busy, m.wakeBlocked)
	}
}
