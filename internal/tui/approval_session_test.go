package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func sessionApprovalModel(t *testing.T) (*terminalModel, *tool.ApprovalGate) {
	t.Helper()
	m := testModel(t)
	gate := tool.NewApprovalGate()
	m.options.Gate = gate
	return m, gate
}

func TestApprovalPanelOffersSessionBeforeRecord(t *testing.T) {
	m, gate := sessionApprovalModel(t)
	dir := t.TempDir()
	request := tool.ApprovalRequest{
		ToolCallID:      "call-session",
		Tool:            "bash",
		WorkDir:         dir,
		Target:          "go test ./...",
		Reason:          "命令不在白名单",
		RecordPrefixes:  [][]string{{"go", "test"}},
		SessionCommands: []tool.CommandGrant{{WorkDir: dir, Prefix: []string{"go", "test"}}},
	}
	response := make(chan tool.ApprovalDecision, 1)
	m.busy = true
	m.beginApproval(&approvalDelivery{ctx: context.Background(), request: request, response: response})
	view := m.View().Content
	for _, want := range []string{"权限确认", "允许本次", "拒绝本次", "本会话内允许", "批准并记录", "本会话授权"} {
		if !strings.Contains(view, want) {
			t.Fatalf("审批面板缺少 %q：%q", want, view)
		}
	}
	order := []string{"允许本次", "拒绝本次", "本会话内允许", "批准并记录"}
	last := -1
	for _, label := range order {
		index := strings.Index(view, label)
		if index <= last {
			t.Fatalf("选项顺序错误 %q：%q", label, view)
		}
		last = index
	}
	if !strings.Contains(view, "› 拒绝本次") {
		t.Fatalf("默认焦点应在拒绝：%q", view)
	}

	// 拒绝 → 本会话内允许。
	m.Update(keyPress(tea.KeyDown))
	m.Update(keyPress(tea.KeyEnter))
	decision := <-response
	if !decision.Allow || !decision.Session || !decision.SessionStored || decision.Record || decision.Source != "tui" {
		t.Fatalf("会话决策异常：%#v", decision)
	}
	if !gate.TrySession(request) {
		t.Fatal("选择后应写入会话授权")
	}
	if m.approval != nil || !strings.Contains(m.status, "已允许本会话") {
		t.Fatalf("审批后状态异常：approval=%v status=%q", m.approval, m.status)
	}
}

func TestApprovalPanelRecordStillReachableAfterSessionOption(t *testing.T) {
	m, _ := sessionApprovalModel(t)
	dir := t.TempDir()
	request := tool.ApprovalRequest{
		Tool:            "bash",
		WorkDir:         dir,
		Target:          "go test ./...",
		Reason:          "命令不在白名单",
		RecordPrefixes:  [][]string{{"go", "test"}},
		SessionCommands: []tool.CommandGrant{{WorkDir: dir, Prefix: []string{"go", "test"}}},
	}
	response := make(chan tool.ApprovalDecision, 1)
	m.beginApproval(&approvalDelivery{request: request, response: response})
	// 拒绝 → 本会话内允许 → 批准并记录。
	m.Update(keyPress(tea.KeyDown))
	m.Update(keyPress(tea.KeyDown))
	m.Update(keyPress(tea.KeyEnter))
	decision := <-response
	if !decision.Allow || decision.Session || !decision.Record {
		t.Fatalf("应仍能选择批准并记录：%#v", decision)
	}
}

func TestApprovalPanelHidesSessionWithoutKey(t *testing.T) {
	m, _ := sessionApprovalModel(t)
	m.beginApproval(&approvalDelivery{
		request:  tool.ApprovalRequest{Tool: "remember", Target: "notes", Reason: "写入长期记忆需要审批"},
		response: make(chan tool.ApprovalDecision, 1),
	})
	view := m.View().Content
	if strings.Contains(view, "本会话内允许") || strings.Contains(view, "本会话授权") {
		t.Fatalf("没有授权键时不应显示会话选项：%q", view)
	}
	if !strings.Contains(view, "› 拒绝本次") {
		t.Fatalf("默认焦点应在拒绝：%q", view)
	}
}

func TestApprovalQueueRechecksSessionGrantBeforeShowingNext(t *testing.T) {
	m, gate := sessionApprovalModel(t)
	dir := t.TempDir()
	request := tool.ApprovalRequest{
		ToolCallID:      "call-1",
		Tool:            "bash",
		WorkDir:         dir,
		Target:          "./build.sh --fast",
		Reason:          "命令不在白名单",
		SessionCommands: []tool.CommandGrant{{WorkDir: dir, Argv: []string{"./build.sh", "--fast"}}},
	}
	first := make(chan tool.ApprovalDecision, 1)
	second := make(chan tool.ApprovalDecision, 1)
	m.beginApproval(&approvalDelivery{ctx: context.Background(), request: request, response: first})
	queued := request
	queued.ToolCallID = "call-2"
	m.beginApproval(&approvalDelivery{ctx: context.Background(), request: queued, response: second})
	if m.approval == nil || len(m.approvalQueue) != 1 {
		t.Fatalf("第二个请求应排队：approval=%v queue=%d", m.approval, len(m.approvalQueue))
	}

	m.Update(keyPress(tea.KeyDown))
	m.Update(keyPress(tea.KeyEnter))
	if decision := <-first; !decision.Allow || !decision.Session || !decision.SessionStored {
		t.Fatalf("第一个请求应创建会话授权：%#v", decision)
	}
	if !gate.TrySession(request) {
		t.Fatal("授权未写入")
	}
	select {
	case decision := <-second:
		if !decision.Allow || decision.Source != tool.ApprovalSourceSession {
			t.Fatalf("队列中的同键请求应自动放行：%#v", decision)
		}
	default:
		t.Fatal("队列中的同键请求没有自动放行")
	}
	if m.approval != nil || len(m.approvalQueue) != 0 {
		t.Fatalf("自动放行后不应继续显示审批：approval=%v queue=%d", m.approval, len(m.approvalQueue))
	}
}

func TestApprovalAutoAllowsAlreadyGrantedRequest(t *testing.T) {
	m, gate := sessionApprovalModel(t)
	dir := t.TempDir()
	request := tool.ApprovalRequest{
		Tool:            "bash",
		WorkDir:         dir,
		Target:          "echo hi",
		Reason:          "命令不在白名单",
		SessionCommands: []tool.CommandGrant{{WorkDir: dir, Argv: []string{"echo", "hi"}}},
	}
	if err := gate.StoreSession(request); err != nil {
		t.Fatal(err)
	}
	response := make(chan tool.ApprovalDecision, 1)
	m.beginApproval(&approvalDelivery{ctx: context.Background(), request: request, response: response})
	if m.approval != nil {
		t.Fatal("已授权请求不应显示审批面板")
	}
	select {
	case decision := <-response:
		if !decision.Allow || decision.Source != tool.ApprovalSourceSession {
			t.Fatalf("已授权请求应自动放行：%#v", decision)
		}
	default:
		t.Fatal("已授权请求没有自动放行")
	}
}

func TestApprovalSessionStoreFailureFallsBackToAllowOnce(t *testing.T) {
	m, gate := sessionApprovalModel(t)
	request := tool.ApprovalRequest{
		Tool:            "bash",
		WorkDir:         t.TempDir(),
		Target:          "./build.sh",
		Reason:          "命令不在白名单",
		SessionCommands: []tool.CommandGrant{{WorkDir: t.TempDir(), Prefix: []string{"./build.sh"}}},
	}
	response := make(chan tool.ApprovalDecision, 1)
	m.busy = true
	m.beginApproval(&approvalDelivery{request: request, response: response})
	m.Update(keyPress(tea.KeyDown))
	m.Update(keyPress(tea.KeyEnter))
	decision := <-response
	if !decision.Allow || decision.Session {
		t.Fatalf("保存失败时应仅允许本次：%#v", decision)
	}
	if !strings.Contains(decision.Reason, "会话") || !strings.Contains(m.status, "会话授权失败") {
		t.Fatalf("应说明会话授权失败：%#v status=%q", decision, m.status)
	}
	if commands, paths := gate.Session().Len(); commands != 0 || paths != 0 {
		t.Fatalf("保存失败不应留下授权：%d %d", commands, paths)
	}
}

func TestApprovalCanceledBeforeEnterDoesNotStoreOrSend(t *testing.T) {
	m, gate := sessionApprovalModel(t)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	request := tool.ApprovalRequest{
		Tool:    "bash",
		WorkDir: dir,
		Target:  "echo hi",
		Reason:  "命令不在白名单",
		SessionCommands: []tool.CommandGrant{{
			WorkDir: dir,
			Argv:    []string{"echo", "hi"},
		}},
	}
	response := make(chan tool.ApprovalDecision, 1)
	m.beginApproval(&approvalDelivery{ctx: ctx, request: request, response: response})
	if m.approval == nil {
		t.Fatal("取消前应正常显示审批")
	}
	cancel()
	// 拒绝 → 本会话内允许。
	m.Update(keyPress(tea.KeyDown))
	m.Update(keyPress(tea.KeyEnter))
	select {
	case decision := <-response:
		t.Fatalf("已取消的请求不应回传决策：%#v", decision)
	default:
	}
	if m.approval != nil {
		t.Fatal("已取消的审批应从面板移除")
	}
	if commands, paths := gate.Session().Len(); commands != 0 || paths != 0 {
		t.Fatalf("已取消的审批不应保存授权：%d %d", commands, paths)
	}
}

func TestApprovalCanceledQueuedRequestIsSkipped(t *testing.T) {
	m, _ := sessionApprovalModel(t)
	firstResponse := make(chan tool.ApprovalDecision, 1)
	m.beginApproval(&approvalDelivery{
		ctx:      context.Background(),
		request:  tool.ApprovalRequest{Tool: "remember", Target: "a", Reason: "需要审批"},
		response: firstResponse,
	})
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	secondResponse := make(chan tool.ApprovalDecision, 1)
	m.beginApproval(&approvalDelivery{
		ctx:      secondCtx,
		request:  tool.ApprovalRequest{Tool: "remember", Target: "b", Reason: "需要审批"},
		response: secondResponse,
	})
	if m.approval == nil || len(m.approvalQueue) != 1 {
		t.Fatalf("第二个请求应排队：approval=%v queue=%d", m.approval, len(m.approvalQueue))
	}
	cancelSecond()
	m.Update(keyPress(tea.KeyEnter)) // 默认拒绝第一个
	if decision := <-firstResponse; decision.Allow {
		t.Fatalf("默认回车应拒绝：%#v", decision)
	}
	select {
	case decision := <-secondResponse:
		t.Fatalf("已取消的排队请求不应回传决策：%#v", decision)
	default:
	}
	if m.approval != nil || len(m.approvalQueue) != 0 {
		t.Fatalf("已取消的排队请求不应显示：approval=%v queue=%d", m.approval, len(m.approvalQueue))
	}
}

func TestApprovalAutoAllowSkipsCanceledRequest(t *testing.T) {
	m, gate := sessionApprovalModel(t)
	dir := t.TempDir()
	request := tool.ApprovalRequest{
		Tool:            "bash",
		WorkDir:         dir,
		Target:          "echo hi",
		SessionCommands: []tool.CommandGrant{{WorkDir: dir, Argv: []string{"echo", "hi"}}},
	}
	if err := gate.StoreSession(request); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response := make(chan tool.ApprovalDecision, 1)
	m.beginApproval(&approvalDelivery{ctx: ctx, request: request, response: response})
	if m.approval != nil {
		t.Fatal("已取消的请求不应显示审批")
	}
	select {
	case decision := <-response:
		t.Fatalf("已取消的请求不应自动回传决策：%#v", decision)
	default:
	}
}

func TestApprovalRequestPrefersCommittedDecisionAfterCancel(t *testing.T) {
	m := testModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan struct {
		decision tool.ApprovalDecision
		err      error
	}, 1)
	go func() {
		decision, err := m.requestApproval(ctx, tool.ApprovalRequest{Tool: "bash", Target: "x"})
		out <- struct {
			decision tool.ApprovalDecision
			err      error
		}{decision, err}
	}()
	var item delivery
	select {
	case item = <-m.events:
	case <-time.After(time.Second):
		t.Fatal("审批请求未到达 TUI")
	}
	approval := item.approval
	if approval == nil {
		t.Fatal("delivery 缺少审批项")
	}
	// 模拟用户点击已提交、随后 ctx 才被取消：在持锁状态取消并提交。
	approval.mu.Lock()
	cancel()
	approval.decided = true
	approval.decision = tool.ApprovalDecision{
		Allow: true, Session: true, SessionStored: true, Source: "tui", Reason: "本会话内允许",
	}
	approval.response <- approval.decision
	approval.mu.Unlock()

	select {
	case got := <-out:
		if got.err != nil || !got.decision.Allow || !got.decision.SessionStored {
			t.Fatalf("已提交的决策不应被取消覆盖：%#v %v", got.decision, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("requestApproval 未返回已提交的决策")
	}
}

func TestApprovalRequestCanceledWithoutCommitReturnsContextError(t *testing.T) {
	m := testModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan error, 1)
	go func() {
		_, err := m.requestApproval(ctx, tool.ApprovalRequest{Tool: "bash", Target: "x"})
		out <- err
	}()
	select {
	case <-m.events:
	case <-time.After(time.Second):
		t.Fatal("审批请求未到达 TUI")
	}
	cancel()
	select {
	case err := <-out:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("未提交决策时应返回 context.Canceled：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("requestApproval 未返回取消错误")
	}
}
