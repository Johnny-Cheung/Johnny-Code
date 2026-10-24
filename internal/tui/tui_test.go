package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
	"github.com/charmbracelet/x/ansi"
)

func keyPress(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func ctrlPress(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: tea.ModCtrl}
}

func testModel(t *testing.T) *terminalModel {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	output := &checkedOutput{Writer: io.Discard, cancel: cancel}
	m := newModel(ctx, Options{}, output, &workers)
	t.Cleanup(func() { cancel(); workers.Wait() })
	return m
}

func TestInputPasteNewlineAndClear(t *testing.T) {
	m := testModel(t)
	m.Update(tea.PasteMsg{Content: "分析下面代码：\nfunc main() {}"})
	if m.busy || m.input.Value() != "分析下面代码：\nfunc main() {}" {
		t.Fatalf("粘贴不得发送或丢失换行：%q", m.input.Value())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	if m.input.Value() != "分析下面代码：\nfunc main() {}\n" {
		t.Fatalf("Alt+Enter 应换行：%q", m.input.Value())
	}
	m.Update(ctrlPress('c'))
	if m.input.Value() != "" || m.quitting || m.busy {
		t.Fatal("空闲 Ctrl+C 只能清空输入")
	}
	m.Update(keyPress(tea.KeyEnter))
	if m.busy {
		t.Fatal("空输入不能发送")
	}
}

func TestPasteNormalizesNewlinesAndRejectsWholeOversizeInput(t *testing.T) {
	m := testModel(t)
	m.Update(tea.PasteMsg{Content: "first\r\nsecond\rthird"})
	if m.input.Value() != "first\nsecond\nthird" {
		t.Fatalf("粘贴换行转换错误：%q", m.input.Value())
	}
	m.Update(tea.PasteMsg{Content: strings.Repeat("line\n", 10000)})
	if m.input.Value() != "first\nsecond\nthird" || !strings.Contains(m.status, "未插入") {
		t.Fatal("超限粘贴必须整体拒绝，不能静默截断")
	}
}

func TestInputHistoryRestoresDraftAndNavigatesMultiline(t *testing.T) {
	m := testModel(t)
	m.history = []string{"first", "second\nline"}
	m.historyIndex = len(m.history)
	m.input.SetValue("draft")
	m.Update(keyPress(tea.KeyUp))
	if m.input.Value() != "second\nline" {
		t.Fatal(m.input.Value())
	}
	// 多行历史先移动到首行，再按上键才切换到更早任务。
	m.Update(keyPress(tea.KeyUp))
	if m.input.Value() != "second\nline" {
		t.Fatal("多行光标移动不应切换历史")
	}
	m.Update(keyPress(tea.KeyUp))
	if m.input.Value() != "first" {
		t.Fatal(m.input.Value())
	}
	m.Update(keyPress(tea.KeyDown))
	m.Update(keyPress(tea.KeyDown))
	if m.input.Value() != "draft" {
		t.Fatalf("应恢复未发送草稿：%q", m.input.Value())
	}
}

func TestExitKeys(t *testing.T) {
	for _, value := range []string{"", " /exit "} {
		m := testModel(t)
		m.input.SetValue(value)
		key := ctrlPress('d')
		if value != "" {
			key = keyPress(tea.KeyEnter)
		}
		_, cmd := m.Update(key)
		if !m.quitting || cmd == nil {
			t.Fatalf("未退出：%q", value)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatal("缺少退出消息")
		}
	}
	m := testModel(t)
	m.input.SetValue("keep")
	m.Update(ctrlPress('d'))
	if m.quitting {
		t.Fatal("非空 Ctrl+D 不应退出")
	}
}

func TestCancellationWaitsForWorkerAndAllowsNextTurn(t *testing.T) {
	m := testModel(t)
	started, release := make(chan string, 2), make(chan struct{})
	m.options.RunTurn = func(ctx context.Context, prompt string, emit func(Event) error, notice func(string)) (agent.Result, error) {
		started <- prompt
		if prompt == "first" {
			<-ctx.Done()
			<-release
			return agent.Result{}, ctx.Err()
		}
		return agent.Result{Status: agent.StatusCompleted}, nil
	}
	m.input.SetValue("first")
	m.Update(keyPress(tea.KeyEnter))
	<-started
	m.Update(ctrlPress('c'))
	m.Update(tea.PasteMsg{Content: "discard"})
	m.Update(keyPress(tea.KeyEnter))
	if !m.busy || !m.stopping || m.input.Value() != "" {
		t.Fatal("清理完成前不能恢复输入或排队任务")
	}
	close(release)
	select {
	case item := <-m.events:
		m.receive(item)
	case <-time.After(time.Second):
		t.Fatal("取消后任务未收尾")
	}
	if m.busy || !strings.Contains(m.status, "已取消") {
		t.Fatalf("应恢复输入：%s", m.status)
	}
	m.input.SetValue("second")
	m.Update(keyPress(tea.KeyEnter))
	if prompt := <-started; prompt != "second" {
		t.Fatal(prompt)
	}
	select {
	case item := <-m.events:
		m.receive(item)
	case <-time.After(time.Second):
		t.Fatal("下一轮未完成")
	}
}

func TestHeaderShowsPermissionMode(t *testing.T) {
	m := testModel(t)
	m.options.Model = "test-model"
	m.options.WorkDir = "/tmp/work"
	m.options.Version = "0.7.0"
	m.options.Mode = "只读"
	header := m.headerText()
	for _, want := range []string{"Johnny-Code · v0.7.0", "权限：只读", "模型：test-model", "目录：/tmp/work"} {
		if !strings.Contains(header, want) {
			t.Fatalf("TUI header 缺少 %q：%q", want, header)
		}
	}
	m.options.Mode = ""
	if strings.Contains(m.headerText(), "权限：") {
		t.Fatal("未设置权限模式时不应显示空权限行")
	}
}

func TestMarkdownStreamRendersCompletedBlocks(t *testing.T) {
	m := testModel(t)
	m.width = 44
	m.markdown.SetWidth(m.contentWidth())
	source := "# 标题\n\n## 小节\n\n第一段 **粗体** 文本，包含中文和 English。\n\n```go\nfunc main() {\n\tfmt.Println(\"你好\")\n}\n```\n\n- 项目一\n- 项目二\n"
	runes := []rune(source)
	var rendered strings.Builder
	appendRendered := func(part string) {
		part = strings.TrimRight(part, "\n")
		if part == "" {
			return
		}
		if rendered.Len() > 0 {
			rendered.WriteString("\n\n")
		}
		rendered.WriteString(part)
	}
	for start := 0; start < len(runes); start += 3 {
		end := min(len(runes), start+3)
		appendRendered(m.markdown.Write(string(runes[start:end])))
	}
	appendRendered(m.markdown.Flush())
	out := rendered.String()
	plain := ansi.Strip(out)
	for _, want := range []string{"标题", "小节", "第一段", "粗体", "func main()", `fmt.Println("你好")`, "项目一", "项目二"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("渲染结果缺少 %q：%s", want, plain)
		}
	}
	for _, unwanted := range []string{"# 标题", "## 小节", "**粗体**", "```"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("Markdown 标记未渲染：%q", unwanted)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if ansi.StringWidth(line) > m.contentWidth() {
			t.Fatalf("渲染行超过宽度：%q", ansi.Strip(line))
		}
	}
	if m.markdown.Preview() != "" {
		t.Fatal("提交后仍有未完成预览")
	}
}

func TestApprovalPanelDefaultsToDenyAndSupportsRecord(t *testing.T) {
	m := testModel(t)
	m.busy = true
	response := make(chan tool.ApprovalDecision, 1)
	m.beginApproval(&approvalDelivery{
		request: tool.ApprovalRequest{
			ToolCallID: "call-1", Tool: "bash", WorkDir: "/tmp/work",
			Target: "go test ./...", Command: "go test ./...", Reason: "命令不在白名单",
			RecordPrefixes: [][]string{{"go", "test"}},
		},
		response: response,
	})
	view := m.View().Content
	for _, want := range []string{"权限确认", "允许本次", "拒绝本次", "批准并记录", "› 拒绝本次", "将记录", "go test"} {
		if !strings.Contains(view, want) {
			t.Fatalf("审批面板缺少 %q：%q", want, view)
		}
	}
	m.Update(keyPress(tea.KeyDown))
	m.Update(keyPress(tea.KeyEnter))
	decision := <-response
	if !decision.Allow || !decision.Record || decision.Source != "tui" {
		t.Fatalf("记录决策异常：%#v", decision)
	}
	if m.approval != nil || !strings.Contains(m.status, "已允许并记录") {
		t.Fatalf("审批后状态异常：approval=%v status=%q", m.approval, m.status)
	}
}

func TestApprovalPanelHidesRecordWhenUnavailable(t *testing.T) {
	m := testModel(t)
	m.busy = true
	m.beginApproval(&approvalDelivery{
		request:  tool.ApprovalRequest{Tool: "bash", Target: "ls; rm x", Reason: "shell 语法"},
		response: make(chan tool.ApprovalDecision, 1),
	})
	view := m.View().Content
	if strings.Contains(view, "批准并记录") {
		t.Fatalf("不可记录时不应显示记录选项：%q", view)
	}
	if !strings.Contains(view, "› 拒绝本次") {
		t.Fatalf("默认焦点应在拒绝：%q", view)
	}
}

func TestApprovalGateWaitsForTUIChoice(t *testing.T) {
	m := testModel(t)
	m.busy = true
	gate := tool.NewApprovalGate()
	gate.Set(m.requestApproval)
	done := make(chan tool.ApprovalDecision, 1)
	errs := make(chan error, 1)
	go func() {
		decision, err := gate.Ask(context.Background(), tool.ApprovalRequest{
			ToolCallID: "call-2", Tool: "read", Target: "/outside/file", Reason: "路径不在白名单",
		})
		done <- decision
		errs <- err
	}()
	select {
	case item := <-m.events:
		m.receive(item)
	case <-time.After(time.Second):
		t.Fatal("审批请求未到达 TUI")
	}
	view := m.View().Content
	if !strings.Contains(view, "路径不在白名单") || !strings.Contains(view, "› 拒绝本次") {
		t.Fatalf("审批面板内容异常：%q", view)
	}
	m.Update(keyPress(tea.KeyEnter))
	select {
	case decision := <-done:
		if decision.Allow {
			t.Fatalf("默认回车不应允许：%#v", decision)
		}
	case <-time.After(time.Second):
		t.Fatal("TUI 未回传审批决策")
	}
	if err := <-errs; err != nil {
		t.Fatalf("审批返回错误：%v", err)
	}
}

func TestApprovalCtrlCCancelsTurn(t *testing.T) {
	m := testModel(t)
	m.busy = true
	turnCtx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	m.beginApproval(&approvalDelivery{
		request:  tool.ApprovalRequest{Tool: "bash", Target: "make test", Reason: "需要审批"},
		response: make(chan tool.ApprovalDecision, 1),
	})
	m.Update(ctrlPress('c'))
	if m.approval != nil || !m.stopping {
		t.Fatalf("Ctrl+C 应取消待审批操作：approval=%v stopping=%v", m.approval, m.stopping)
	}
	if turnCtx.Err() == nil {
		t.Fatal("审批期间 Ctrl+C 应取消本轮 context")
	}
}

func TestLocalCommandDoesNotStartTurn(t *testing.T) {
	m := testModel(t)
	m.options.LocalCommand = func(line string) (LocalCommandResult, bool) {
		if line != "/permissions" {
			return LocalCommandResult{}, false
		}
		return LocalCommandResult{Output: "权限列表"}, true
	}
	m.input.SetValue("/permissions")
	_, cmd := m.Update(keyPress(tea.KeyEnter))
	if m.busy {
		t.Fatal("本地命令不应启动任务")
	}
	if m.input.Value() != "" {
		t.Fatalf("本地命令后输入未清空：%q", m.input.Value())
	}
	if cmd == nil {
		t.Fatal("本地命令应输出结果")
	}
}

func TestLocalCommandExpandedPromptKeepsDisplay(t *testing.T) {
	m := testModel(t)
	received := make(chan string, 1)
	m.options.LocalCommand = func(line string) (LocalCommandResult, bool) {
		if line != "/skill demo 参数" {
			return LocalCommandResult{}, false
		}
		return LocalCommandResult{Prompt: "展开后的 Skill 正文"}, true
	}
	m.options.RunTurn = func(_ context.Context, prompt string, _ func(Event) error, _ func(string)) (agent.Result, error) {
		received <- prompt
		return agent.Result{Status: agent.StatusCompleted}, nil
	}
	m.input.SetValue("/skill demo 参数")
	m.Update(keyPress(tea.KeyEnter))
	select {
	case got := <-received:
		if got != "展开后的 Skill 正文" {
			t.Fatalf("模型收到的任务错误：%q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("展开的本地命令未启动任务")
	}
	if !m.busy {
		t.Fatal("展开的本地命令应进入执行状态")
	}
	if len(m.history) == 0 || m.history[len(m.history)-1] != "/skill demo 参数" {
		t.Fatalf("输入历史应保留用户原始命令：%v", m.history)
	}
}

func TestBusyViewShowsProgressWithoutStreamingAnswer(t *testing.T) {
	m := testModel(t)
	m.busy = true
	m.receive(delivery{event: Event{Kind: agent.EventToolStart, ID: "1", Name: "bash", Key: "go test ./..."}})
	m.receive(delivery{event: Event{Kind: agent.EventModel, Text: "不应该提前出现在滚动历史里的最终回答内容"}})
	for _, size := range []tea.WindowSizeMsg{{Width: 40, Height: 12}, {Width: 18, Height: 5}, {Width: 4, Height: 2}} {
		m.Update(size)
		view := m.View().Content
		if strings.Contains(view, "不应该提前出现") {
			t.Fatalf("执行期间不应把最终回答作为原文预览：%q", view)
		}
		if size.Width >= 10 && !strings.Contains(view, "bash") {
			t.Fatalf("执行中应显示工具进展：%q", view)
		}
		if lines := strings.Split(view, "\n"); len(lines) > size.Height {
			t.Fatalf("动态区域高于窗口：%q", view)
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size.Width {
				t.Fatalf("动态区域宽于窗口：%q", line)
			}
		}
	}
}

func TestUnlabeledCodeBlockIsPlainAndWordWrapped(t *testing.T) {
	m := testModel(t)
	m.width = 61
	m.markdown.SetWidth(m.contentWidth())
	source := "```\nbackend/\n├── internal/\n│   └── api/  HTTP Handler: auth、friend、group、moment、msg_op、avatar、upload、settings\n```\n"
	out := m.markdown.Write(source) + m.markdown.Flush()
	if strings.Contains(out, "\x1b[48;") {
		t.Fatalf("未标注代码块不应带错误背景：%q", out)
	}
	plain := ansi.Strip(out)
	for _, want := range []string{"backend/", "settings", "HTTP Handler"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("纯文本代码块缺少 %q：%q", want, plain)
		}
	}
	if strings.Contains(plain, "setti\nngs") {
		t.Fatalf("代码块不应从单词中间断开：%q", plain)
	}
	for _, line := range strings.Split(out, "\n") {
		if ansi.StringWidth(line) > m.contentWidth() {
			t.Fatalf("代码行超过终端宽度：%q", ansi.Strip(line))
		}
	}
}

func TestLiveToolProgressUsesAvailableHeight(t *testing.T) {
	m := testModel(t)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	m.busy = true
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("call-%02d", i)
		name := fmt.Sprintf("tool%02d", i)
		m.receive(delivery{event: Event{Kind: agent.EventToolStart, ID: id, Name: name, Key: "argument"}})
		m.receive(delivery{event: Event{Kind: agent.EventToolEnd, ID: id, Name: name}})
	}
	view := m.View().Content
	if strings.Count(view, "✓") < 7 {
		t.Fatalf("应使用可用高度显示超过 6 条工具记录：%q", view)
	}
	if !strings.Contains(view, "tool09") || !strings.Contains(view, "更早 2 条工具记录") {
		t.Fatalf("应显示最新记录和更早条数提示：%q", view)
	}
	if lines := strings.Split(view, "\n"); len(lines) > 11 {
		t.Fatalf("动态区域应留出滚动余量：%q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 60 {
			t.Fatalf("动态区域超出宽度：%q", line)
		}
	}
}

func TestIdleViewHasTopAndBottomInputBorders(t *testing.T) {
	m := testModel(t)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	view := m.View()
	lines := strings.Split(view.Content, "\n")
	var borders []int
	for i, line := range lines {
		plain := strings.TrimSpace(ansi.Strip(line))
		if plain != "" && strings.Trim(plain, "─") == "" {
			borders = append(borders, i)
		}
	}
	if len(borders) != 2 || borders[0] >= borders[1] {
		t.Fatalf("输入框需要上下两条横线：%q", view.Content)
	}
	if !strings.Contains(ansi.Strip(lines[len(lines)-1]), "Enter 发送") {
		t.Fatal("状态提示应在下横线之后")
	}
}

func TestRealCursorFollowsInputForIME(t *testing.T) {
	m := testModel(t)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	m.Update(tea.PasteMsg{Content: "你好"})
	view := m.View()
	if view.Cursor == nil {
		t.Fatal("空闲输入必须提供真实光标，输入法才能定位组合文本")
	}
	if view.Cursor.X != 6 || view.Cursor.Y != 1 {
		t.Fatalf("光标应在上边框之后的输入位置：X=%d Y=%d", view.Cursor.X, view.Cursor.Y)
	}
	m.busy = true
	if m.View().Cursor != nil {
		t.Fatal("任务执行时不应显示输入光标")
	}
}

func TestLiveToolProgressCollapsesAndExpands(t *testing.T) {
	m := testModel(t)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 18})
	m.busy = true
	m.receive(delivery{event: Event{Kind: agent.EventToolStart, ID: "1", Name: "write", Key: "main.go"}})
	m.receive(delivery{event: Event{Kind: agent.EventToolEnd, ID: "1", Name: "write", Text: "已创建 main.go"}})
	m.receive(delivery{event: Event{Kind: agent.EventToolStart, ID: "2", Name: "bash", Key: "go test ./..."}})
	m.receive(delivery{event: Event{Kind: agent.EventToolEnd, ID: "2", Name: "bash", Failed: true, Text: "退出码 1"}})
	view := m.View().Content
	for _, want := range []string{"✓ write", "✗ bash", "main.go", "go test ./..."} {
		if !strings.Contains(view, want) {
			t.Fatalf("执行中的进展缺少 %q：%q", want, view)
		}
	}
	summary := m.toolSummaryText()
	for _, want := range []string{"已折叠 2 次工具调用", "write", "bash", "1 次未成功", "Ctrl+O 展开"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("摘要缺少 %q：%q", want, summary)
		}
	}
	m.receive(delivery{done: true})
	if len(m.liveTools) != 0 || len(m.lastTools) != 2 || m.expanded {
		t.Fatalf("完成后应清空实时进展并保留可展开记录：live=%d last=%d expanded=%v", len(m.liveTools), len(m.lastTools), m.expanded)
	}
	if !strings.Contains(m.View().Content, "Ctrl+O 展开工具过程") {
		t.Fatal("折叠后应提示 Ctrl+O 展开")
	}
	expanded := m.expansionText()
	for _, want := range []string{"最近一次工具过程", "✓ write", "已创建 main.go", "✗ bash", "退出码 1"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("展开内容缺少 %q：%q", want, expanded)
		}
	}
	m.Update(ctrlPress('o'))
	if !m.expanded {
		t.Fatal("Ctrl+O 后应标记已展开")
	}
	if strings.Contains(m.View().Content, "Ctrl+O 展开工具过程") {
		t.Fatal("已展开后不应继续提示 Ctrl+O")
	}
}

func TestResizeAndControlCharacters(t *testing.T) {
	m := testModel(t)
	text := safeText("code\n\t中文\x1b[2J\x07\r")
	if strings.ContainsAny(text, "\x1b\x07\r") || !strings.Contains(text, "\n    中文") {
		t.Fatalf("不安全或丢失换行：%q", text)
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 10, Height: 5}, {Width: 1, Height: 1}} {
		m.Update(size)
		m.input.SetValue("中文输入\nnext")
		m.resizeInput()
		view := m.View().Content
		if len(strings.Split(view, "\n")) > size.Height {
			t.Fatalf("界面高于窗口：%q", view)
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size.Width {
				t.Fatalf("界面宽于窗口：%q", line)
			}
		}
	}
}

type shortOutput struct{}

func (shortOutput) Write(data []byte) (int, error) { return 0, nil }

func TestOutputFailureCancelsSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &checkedOutput{Writer: shortOutput{}, cancel: cancel}
	_, err := output.Write([]byte("tool"))
	if !errors.Is(err, io.ErrShortWrite) || ctx.Err() == nil {
		t.Fatalf("短写未传播或未取消会话：%v", err)
	}
	if _, next := output.Write([]byte("tool")); !errors.Is(next, io.ErrShortWrite) {
		t.Fatal("后续写入应收到原始写错误")
	}
}

func TestToolStartStatusRenderedBeforeExecution(t *testing.T) {
	m := testModel(t)
	executed := make(chan struct{}, 1)
	m.options.RunTurn = func(ctx context.Context, prompt string, emit func(Event) error, notice func(string)) (agent.Result, error) {
		if err := emit(Event{Kind: agent.EventToolStart, Name: "bash", Key: "echo hello"}); err != nil {
			return agent.Result{}, err
		}
		executed <- struct{}{}
		return agent.Result{}, nil
	}
	m.start("run")
	item := <-m.events
	m.receive(item)
	select {
	case <-executed:
		t.Fatal("状态渲染前已执行")
	default:
	}
	view := m.View().Content
	if !strings.Contains(view, "正在执行 bash") {
		t.Fatalf("状态区应显示当前工具：%q", view)
	}
	m.Update(toolStatusShownMsg{})
	select {
	case <-executed:
	case <-time.After(time.Second):
		t.Fatal("状态更新后未继续")
	}
	m.receive(<-m.events)
}

func TestToolStartWriteFailurePreventsExecution(t *testing.T) {
	m := testModel(t)
	m.output.Writer = shortOutput{}
	executed := make(chan struct{}, 1)
	m.options.RunTurn = func(ctx context.Context, prompt string, emit func(Event) error, notice func(string)) (agent.Result, error) {
		if err := emit(Event{Kind: agent.EventToolStart, Name: "write", Key: "never.txt"}); err != nil {
			return agent.Result{}, err
		}
		executed <- struct{}{}
		return agent.Result{}, nil
	}
	m.start("run")
	item := <-m.events
	m.receive(item)
	_, _ = m.output.Write([]byte(m.View().Content))
	m.Update(toolStatusShownMsg{})
	m.workers.Wait()
	select {
	case <-executed:
		t.Fatal("终端短写后工具仍被执行")
	default:
	}
}

func TestWatchedInputOnlyPreservesRealFileInterface(t *testing.T) {
	nonFile := watchInput(strings.NewReader(""), func(error) {})
	if _, ok := nonFile.(interface{ Fd() uintptr }); ok {
		t.Fatal("普通 Reader 不应宣称自己是终端文件")
	}
	file, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	watched := watchInput(file, func(error) {})
	terminalFile, ok := watched.(interface {
		io.ReadWriteCloser
		Fd() uintptr
		Name() string
	})
	if !ok || terminalFile.Fd() != file.Fd() {
		t.Fatal("文件接口必须完整保留")
	}
}

func TestInputEOFFinishesUI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := Run(ctx, Options{Input: strings.NewReader(""), Output: io.Discard,
		RunTurn: func(context.Context, string, func(Event) error, func(string)) (agent.Result, error) {
			t.Error("EOF 不应启动任务")
			return agent.Result{}, nil
		},
	})
	if err != nil {
		t.Fatalf("EOF 应正常退出而不是等待取消：%v", err)
	}
}

func TestInputFailureWaitsForCanceledWorker(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started, stopped := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- Run(ctx, Options{Input: reader, Output: io.Discard,
			RunTurn: func(ctx context.Context, prompt string, emit func(Event) error, notice func(string)) (agent.Result, error) {
				close(started)
				<-ctx.Done()
				close(stopped)
				return agent.Result{}, ctx.Err()
			},
		})
	}()
	if _, err := io.WriteString(writer, "task\r"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("未启动任务")
	}
	_ = writer.CloseWithError(io.ErrUnexpectedEOF)
	select {
	case err := <-finished:
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("输入错误未传播：%v", err)
		}
	case <-ctx.Done():
		t.Fatal("输入失败后未停止")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("界面退出后仍有运行任务")
	}
}
