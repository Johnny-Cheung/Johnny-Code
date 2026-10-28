// Package tui provides an inline terminal interface over the existing Agent.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
	"github.com/charmbracelet/x/ansi"
)

// Event 只携带有界展示数据；完整响应和续接数据仍由 Agent 和诊断记录持有。
type Event struct {
	Kind   agent.EventType
	ID     string
	Text   string
	Name   string
	Key    string
	Failed bool
	Final  bool
}

type toolRecord struct {
	ID     string
	Name   string
	Key    string
	State  string
	Detail string
}

type Options struct {
	Input         io.Reader
	Output        io.Writer
	Model         string
	WorkDir       string
	Version       string
	Mode          string
	Gate          *tool.ApprovalGate
	LocalCommand  func(string) (LocalCommandResult, bool)
	SlashCommands func() []SlashCommand
	RunTurn       func(context.Context, string, func(Event) error, func(string)) (agent.Result, error)
	RunCompact    func(context.Context, func(string)) (string, error)
	// Subagents 非空时显示后台子任务区、处理其审批并支持唤醒与统一取消。
	Subagents SubagentControl
	// RunWake 以子任务结果为起点执行一轮（空闲唤醒），不回显为用户输入。
	RunWake func(context.Context, func(Event) error, func(string)) (agent.Result, error)
}

// LocalCommandResult 表示本地命令的结果。Prompt 非空时作为本轮任务发送，
// Compact 为 true 时执行一次手动上下文压缩，否则只显示 Output。
type LocalCommandResult struct {
	Output  string
	Prompt  string
	Compact bool
}

type approvalDelivery struct {
	ctx      context.Context
	request  tool.ApprovalRequest
	response chan tool.ApprovalDecision
}

type approvalOption struct {
	label  string
	allow  bool
	record bool
}

type approvalState struct {
	ctx      context.Context
	request  tool.ApprovalRequest
	response chan tool.ApprovalDecision
	options  []approvalOption
	selected int
}

type delivery struct {
	event       Event
	approval    *approvalDelivery
	notice      string
	ready       chan error
	done        bool
	compactDone bool
	text        string
	result      agent.Result
	err         error
}

// toolStatusShownMsg 标记状态区已经获得一次渲染机会，可以检查输出错误并
// 放行工具执行。
type toolStatusShownMsg struct{}

// printResponseMsg 在清空动态预览后延迟一个渲染周期再写入完整回答，避免
// 旧预览随着大量 scrollback 输出滚入终端历史，造成正文重复。
type printResponseMsg struct{}

type responsePrint struct {
	summary string
	answer  string
}

const (
	toolStatusGrace    = 20 * time.Millisecond
	responseFlushDelay = 30 * time.Millisecond
)

type terminalModel struct {
	options       Options
	ctx           context.Context
	output        *checkedOutput
	events        chan delivery
	workers       *sync.WaitGroup
	input         textarea.Model
	width         int
	height        int
	busy          bool
	stopping      bool
	quitting      bool
	cancel        context.CancelFunc
	status        string
	markdown      markdownStream
	pendingReady  chan error
	liveTools     []toolRecord
	lastTools     []toolRecord
	expanded      bool
	responseRunes int
	responseOut   []string
	pendingPrint  *responsePrint
	history       []string
	historyIndex  int
	draft         string
	approval      *approvalState
	approvalQueue []*approvalDelivery
	slash         slashMenuState
	subs          []subagentView
	subTick       bool
	wakeRounds    int
	wakeBlocked   bool
}

// Run 在退出前取消并等待所有任务。即使库因终端错误提前恢复终端，也不会留下
// 仍在写日志或执行工具的工作线程。按键 Ctrl+C 只取消子任务，不取消此 context。
func Run(ctx context.Context, options Options) error {
	if options.Input == nil || options.Output == nil || options.RunTurn == nil {
		return errors.New("终端输入、输出和任务执行器不能为空")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &checkedOutput{Writer: options.Output, cancel: cancel}
	inputErrors := make(chan error, 1)
	input := watchInput(options.Input, func(err error) {
		select {
		case inputErrors <- err:
		default:
		}
		cancel()
	})
	var workers sync.WaitGroup
	m := newModel(runCtx, options, output, &workers)
	if options.Gate != nil {
		options.Gate.Set(m.requestApproval)
		defer options.Gate.Set(nil)
	}
	program := tea.NewProgram(m, tea.WithInput(input), tea.WithOutput(output),
		tea.WithContext(runCtx), tea.WithoutSignalHandler())
	if options.Subagents != nil {
		unsubscribe := options.Subagents.Subscribe(func(notice SubagentNotice) {
			program.Send(subagentMsg{notice: notice})
		})
		defer unsubscribe()
	}
	_, err := program.Run()
	cancel()
	workers.Wait()
	if outputErr := output.failure(); outputErr != nil {
		return outputErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	select {
	case inputErr := <-inputErrors:
		if errors.Is(inputErr, io.EOF) {
			return nil
		}
		return fmt.Errorf("读取终端输入失败：%w", inputErr)
	default:
	}
	return err
}

func newModel(ctx context.Context, options Options, output *checkedOutput, workers *sync.WaitGroup) *terminalModel {
	input := textarea.New()
	input.Prompt = "❯ "
	input.Placeholder = "输入任务…"
	input.ShowLineNumbers = false
	input.CharLimit = 0
	input.MaxWidth = 0
	input.MaxHeight = 0
	input.SetWidth(78)
	input.SetHeight(1)
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))
	input.KeyMap.Paste.SetEnabled(false) // 终端括号粘贴由 PasteMsg 接收，不访问系统剪贴板。
	styles := input.Styles()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Blurred.CursorLine = lipgloss.NewStyle()
	styles.Focused.Prompt = lipgloss.NewStyle().Bold(true)
	styles.Blurred.Prompt = styles.Focused.Prompt
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styles.Blurred.Placeholder = styles.Focused.Placeholder
	styles.Cursor.Blink = false
	input.SetStyles(styles)
	// 使用真实终端光标而不是字符反转的虚拟光标，输入法组合文本才会出现在
	// 输入位置；View.Cursor 每次按输入框光标计算位置。
	input.SetVirtualCursor(false)
	input.Focus()
	return &terminalModel{options: options, ctx: ctx, output: output, events: make(chan delivery),
		workers: workers, input: input, width: 80, height: 24, status: "空闲",
		markdown: markdownStream{width: 79}}
}

// Init 打印页眉并启动事件读取链。读取链在整个会话期间保持存活（空闲时也是），
// 这样后台子任务的审批请求随时都有接收方。
func (m *terminalModel) Init() tea.Cmd {
	return tea.Batch(tea.Println(wrap(m.headerText(), m.contentWidth())), m.waitEvent())
}

func (m *terminalModel) headerText() string {
	mode := ""
	if m.options.Mode != "" {
		mode = fmt.Sprintf("权限：%s\n", safeText(m.options.Mode))
	}
	return fmt.Sprintf("Johnny-Code · v%s\n%s模型：%s\n目录：%s\n", safeText(m.options.Version), mode, safeText(m.options.Model), safeText(m.options.WorkDir))
}

func (m *terminalModel) contentWidth() int { return max(1, m.width-1) }

func (m *terminalModel) waitEvent() tea.Cmd {
	return func() tea.Msg {
		select {
		case event := <-m.events:
			return event
		case <-m.ctx.Done():
			return tea.Quit()
		}
	}
}

func (m *terminalModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	m.pruneApprovals()
	model, cmd := m.update(message)
	if wake := m.maybeWake(); wake != nil {
		cmd = tea.Batch(cmd, wake)
	}
	return model, cmd
}

func (m *terminalModel) update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case subagentMsg:
		return m, m.applySubagent(msg.notice)
	case subagentTickMsg:
		m.subTick = false
		return m, m.scheduleSubagentTick()
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		m.markdown.SetWidth(m.contentWidth())
		m.resizeInput()
		m.refreshSlashMenu()
		// 尾行留到下一次有序 delivery 再提交，避免 resize 的独立 print
		// 命令与正在到达的正文竞争顺序。View 只暂时裁剪可见部分。
		return m, nil
	case delivery:
		return m, m.receive(msg)
	case toolStatusShownMsg:
		if m.pendingReady != nil {
			ready := m.pendingReady
			m.pendingReady = nil
			if err := m.output.failure(); err != nil {
				ready <- err
			} else {
				ready <- nil
			}
		}
		return m, nil
	case printResponseMsg:
		if m.pendingPrint == nil {
			return m, m.waitEvent()
		}
		pending := m.pendingPrint
		m.pendingPrint = nil
		return m, m.printResponse(pending.summary, pending.answer)
	case tea.PasteMsg:
		if m.busy || m.approval != nil {
			return m, nil
		}
		text := normalizeNewlines(msg.Content)
		// textarea 库有 10000 行硬上限；整批拒绝，避免静默截掉任务尾部。
		if m.input.LineCount()+strings.Count(text, "\n") > 10000 {
			m.status = "输入最多 10000 行，本次粘贴未插入"
			return m, nil
		}
		message = tea.PasteMsg{Content: text}
	case tea.KeyPressMsg:
		keys := msg.String()
		if m.approval != nil {
			return m, m.handleApprovalKey(keys)
		}
		if m.busy {
			if keys == "ctrl+c" && !m.stopping {
				m.stopping = true
				m.status = "正在取消，等待当前操作停止…"
				m.cancel()
				m.cancelSubagents()
			}
			return m, nil
		}
		m.refreshSlashMenu()
		if m.slash.visible {
			switch keys {
			case "up":
				m.moveSlash(-1)
				return m, nil
			case "down":
				m.moveSlash(1)
				return m, nil
			case "tab":
				m.completeSlash()
				return m, nil
			case "esc":
				m.dismissSlash()
				return m, nil
			case "enter":
				if !m.slashExactMatch() {
					m.completeSlash()
					return m, nil
				}
			}
		}
		switch keys {
		case "ctrl+c":
			if m.input.Value() == "" && m.cancelSubagents() > 0 {
				return m, nil
			}
			m.input.Reset()
			m.historyIndex = len(m.history)
			m.draft = ""
			m.status = "空闲"
			m.resizeInput()
			m.refreshSlashMenu()
			return m, nil
		case "ctrl+d":
			if m.input.Value() == "" {
				return m, m.exit()
			}
		case "ctrl+o":
			if !m.expanded && len(m.lastTools) > 0 {
				m.expanded = true
				return m, tea.Println(m.expansionText())
			}
			return m, nil
		case "enter":
			prompt := strings.TrimSpace(m.input.Value())
			if prompt == "" {
				return m, nil
			}
			if prompt == "/exit" {
				return m, m.exit()
			}
			if m.options.LocalCommand != nil {
				if result, handled := m.options.LocalCommand(prompt); handled {
					if result.Compact {
						return m, m.startCompact(prompt)
					}
					if result.Prompt != "" {
						return m, m.startExpanded(result.Prompt, prompt)
					}
					return m, m.showLocalResult(prompt, result.Output)
				}
			}
			return m, m.start(prompt)
		case "alt+enter", "ctrl+j":
			if m.input.LineCount() >= 10000 {
				m.status = "输入最多 10000 行"
				return m, nil
			}
		case "up":
			if m.input.Line() == 0 {
				m.navigateHistory(-1)
				return m, nil
			}
		case "down":
			if m.input.Line() == m.input.LineCount()-1 {
				m.navigateHistory(1)
				return m, nil
			}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(message)
	m.resizeInput()
	m.refreshSlashMenu()
	return m, cmd
}

func normalizeNewlines(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

func (m *terminalModel) resizeInput() {
	m.input.SetWidth(max(3, m.contentWidth()))
	// 极矮终端优先显示输入，footer 和分隔线由 View 按高度省略。
	m.input.SetHeight(min(max(1, m.height-4), min(6, max(1, m.input.LineCount()))))
}

func (m *terminalModel) navigateHistory(direction int) {
	if len(m.history) == 0 {
		return
	}
	if m.historyIndex == len(m.history) && direction < 0 {
		m.draft = m.input.Value()
	}
	m.historyIndex = min(len(m.history), max(0, m.historyIndex+direction))
	value := m.draft
	if m.historyIndex < len(m.history) {
		value = m.history[m.historyIndex]
	}
	m.input.SetValue(value)
	m.resizeInput()
	m.refreshSlashMenu()
}

func (m *terminalModel) start(prompt string) tea.Cmd {
	return m.startExpanded(prompt, prompt)
}

// startExpanded 把 prompt 发送给模型，但历史记录和滚动区只显示 display。
// 用于 /skill 这类本地展开：实际上下文包含完整 Skill 正文，界面仍显示用户输入。
func (m *terminalModel) startExpanded(prompt, display string) tea.Cmd {
	if display == "" {
		display = prompt
	}
	m.beginTurnState("正在等待模型回复…")
	m.history = append(m.history, display)
	m.historyIndex, m.draft = len(m.history), ""
	m.wakeRounds, m.wakeBlocked = 0, false
	run := m.options.RunTurn
	m.launchTurn(func(ctx context.Context, emit func(Event) error, notice func(string)) (agent.Result, error) {
		return run(ctx, prompt, emit, notice)
	})
	return tea.Println(wrap("\n❯ "+safeText(display)+"\n", m.contentWidth()))
}

// beginTurnState 进入一轮任务的忙碌状态：清空上一轮的动态内容并锁定输入。
func (m *terminalModel) beginTurnState(status string) {
	m.busy, m.stopping = true, false
	m.status = status
	m.pendingReady = nil
	m.liveTools = nil
	m.pendingPrint = nil
	m.resetResponse()
	m.input.Reset()
	m.input.Blur()
	m.slash = slashMenuState{}
	m.resizeInput()
}

// launchTurn 在后台执行一轮任务，事件经 m.events 回到界面。
func (m *terminalModel) launchTurn(run func(context.Context, func(Event) error, func(string)) (agent.Result, error)) {
	turnCtx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		defer cancel()
		emit := func(event Event) error {
			if err := m.output.failure(); err != nil {
				return err
			}
			item := delivery{event: event}
			if event.Kind == agent.EventToolStart {
				item.ready = make(chan error, 1)
			}
			// 使用会话 context，允许取消后的工具结果和收尾事件继续显示。
			select {
			case m.events <- item:
			case <-m.ctx.Done():
				return m.ctx.Err()
			}
			if item.ready != nil {
				select {
				case err := <-item.ready:
					return err
				case <-turnCtx.Done():
					return turnCtx.Err()
				}
			}
			return nil
		}
		notice := func(text string) {
			select {
			case m.events <- delivery{notice: text}:
			case <-m.ctx.Done():
			}
		}
		result, err := run(turnCtx, emit, notice)
		select {
		case m.events <- delivery{done: true, result: result, err: err}:
		case <-m.ctx.Done():
		}
	}()
}

// startCompact 在后台生成摘要，期间保持输入区不可编辑，结束后打印结果。
func (m *terminalModel) startCompact(display string) tea.Cmd {
	if m.options.RunCompact == nil {
		return m.showLocalResult(display, "当前会话未启用上下文治理。")
	}
	m.busy, m.stopping = true, false
	m.status = "正在压缩上下文…"
	m.pendingReady = nil
	m.liveTools = nil
	m.pendingPrint = nil
	m.resetResponse()
	m.input.Reset()
	m.input.Blur()
	m.slash = slashMenuState{}
	m.history = append(m.history, display)
	m.historyIndex, m.draft = len(m.history), ""
	m.resizeInput()
	turnCtx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		defer cancel()
		notice := func(text string) {
			select {
			case m.events <- delivery{notice: text}:
			case <-m.ctx.Done():
			}
		}
		text, err := m.options.RunCompact(turnCtx, notice)
		select {
		case m.events <- delivery{compactDone: true, text: text, err: err}:
		case <-m.ctx.Done():
		}
	}()
	return tea.Println(wrap("\n❯ "+safeText(display)+"\n", m.contentWidth()))
}

func (m *terminalModel) requestApproval(ctx context.Context, request tool.ApprovalRequest) (tool.ApprovalDecision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	response := make(chan tool.ApprovalDecision, 1)
	item := delivery{approval: &approvalDelivery{ctx: ctx, request: request, response: response}}
	select {
	case m.events <- item:
	case <-ctx.Done():
		return tool.ApprovalDecision{}, ctx.Err()
	case <-m.ctx.Done():
		return tool.ApprovalDecision{}, m.ctx.Err()
	}
	select {
	case decision := <-response:
		return decision, nil
	case <-ctx.Done():
		return tool.ApprovalDecision{}, ctx.Err()
	case <-m.ctx.Done():
		return tool.ApprovalDecision{}, m.ctx.Err()
	}
}

// beginApproval 收到审批请求：当前没有待处理项时直接显示，否则按先进先出排队。
// 审批可能来自主 Agent，也可能来自后台子任务，与主 Agent 是否忙碌无关。
func (m *terminalModel) beginApproval(item *approvalDelivery) {
	if item == nil {
		return
	}
	if m.approval != nil {
		m.approvalQueue = append(m.approvalQueue, item)
		return
	}
	m.showApproval(item)
}

func (m *terminalModel) showApproval(item *approvalDelivery) {
	options := []approvalOption{
		{label: "允许本次", allow: true},
		{label: "拒绝本次"},
	}
	if item.request.CanRecord() {
		options = append(options, approvalOption{label: "批准并记录", allow: true, record: true})
	}
	m.approval = &approvalState{
		ctx:      item.ctx,
		request:  item.request,
		response: item.response,
		options:  options,
		selected: 1,
	}
	m.status = "等待权限确认"
}

// pruneApprovals 丢弃请求方已经取消的审批（本轮被取消、子任务被取消或超时），
// 并在当前项被移除后显示队列中的下一项。
func (m *terminalModel) pruneApprovals() {
	if m.approval == nil && len(m.approvalQueue) == 0 {
		return
	}
	if m.approval != nil && m.approval.ctx != nil && m.approval.ctx.Err() != nil {
		m.approval = nil
	}
	alive := m.approvalQueue[:0]
	for _, item := range m.approvalQueue {
		if item.ctx == nil || item.ctx.Err() == nil {
			alive = append(alive, item)
		}
	}
	m.approvalQueue = alive
	m.promoteApproval()
}

// promoteApproval 在没有当前审批时显示队列头；队列也空了则恢复空闲状态文字。
func (m *terminalModel) promoteApproval() {
	if m.approval != nil {
		return
	}
	if len(m.approvalQueue) > 0 {
		next := m.approvalQueue[0]
		m.approvalQueue = m.approvalQueue[1:]
		m.showApproval(next)
		return
	}
	if !m.busy && m.status == "等待权限确认" {
		m.status = "空闲"
	}
}

func (m *terminalModel) handleApprovalKey(keys string) tea.Cmd {
	state := m.approval
	if state == nil {
		return nil
	}
	switch keys {
	case "ctrl+c":
		// 取消主 Agent 当前轮（如果有）和全部子任务，它们的待审批请求一并丢弃。
		m.approval = nil
		m.approvalQueue = nil
		if m.busy && !m.stopping {
			m.stopping = true
			m.status = "正在取消，等待当前操作停止…"
			m.cancel()
		}
		m.cancelSubagents()
		m.promoteApproval()
	case "up", "k":
		state.selected = (state.selected + len(state.options) - 1) % len(state.options)
	case "down", "j", "tab":
		state.selected = (state.selected + 1) % len(state.options)
	case "enter":
		option := state.options[state.selected]
		select {
		case state.response <- tool.ApprovalDecision{
			Allow:  option.allow,
			Record: option.record,
			Source: "tui",
			Reason: option.label,
		}:
		default:
		}
		m.approval = nil
		switch {
		case !option.allow:
			m.status = "已拒绝本次，等待模型继续…"
		case option.record:
			m.status = "已允许并记录，等待工具执行…"
		default:
			m.status = "已允许本次，等待工具执行…"
		}
		if !m.busy {
			m.status = "空闲"
		}
		m.promoteApproval()
	}
	return nil
}

func (m *terminalModel) showLocalResult(prompt, text string) tea.Cmd {
	m.input.Reset()
	m.input.Focus()
	m.slash = slashMenuState{}
	m.history = append(m.history, prompt)
	m.historyIndex, m.draft = len(m.history), ""
	m.resizeInput()
	body := "❯ " + safeText(prompt)
	if text != "" {
		body += "\n" + wrap(safeText(text), m.contentWidth())
	}
	return tea.Println(body)
}

func (m *terminalModel) resetResponse() {
	m.responseRunes = 0
	m.responseOut = nil
	m.markdown.Reset()
}

func (m *terminalModel) receive(item delivery) tea.Cmd {
	if item.done {
		return m.finishTurn(item)
	}
	if item.compactDone {
		return m.finishCompact(item)
	}
	if item.approval != nil {
		m.beginApproval(item.approval)
		return m.waitEvent()
	}
	if item.notice != "" {
		notice := wrap(safeText(strings.TrimRight(item.notice, "\n")), m.contentWidth())
		return tea.Sequence(tea.Println(notice), m.waitEvent())
	}
	event := item.event
	switch event.Kind {
	case agent.EventRequestStart:
		// 每次模型响应独立缓冲；最终回答之前先把工具过程折叠成摘要。
		m.resetResponse()
		if !m.stopping {
			m.status = "正在等待模型回复…"
		}
	case agent.EventNotice:
		if event.Text != "" {
			m.status = safeText(event.Text)
			notice := wrap(safeText(event.Text), m.contentWidth())
			return tea.Sequence(tea.Println(notice), m.waitEvent())
		}
	case agent.EventModel:
		if event.Text != "" {
			m.responseRunes += utf8.RuneCountInString(event.Text)
			if !m.stopping {
				m.status = fmt.Sprintf("正在接收回复…（%d 字）", m.responseRunes)
			}
			rendered := m.markdown.Write(event.Text)
			if rendered != "" {
				m.responseOut = append(m.responseOut, rendered)
			}
		}
	case agent.EventResponse:
		return m.finishResponse(event)
	case agent.EventEnd:
		// 正常响应已在 EventResponse 提交；done 处理异常时残留的内容。
	case agent.EventToolStart:
		m.beginTool(event)
		if !m.stopping {
			m.status = "正在执行 " + safeText(event.Name) + "…"
		}
		if item.ready != nil {
			m.pendingReady = item.ready
			return tea.Sequence(
				tea.Tick(toolStatusGrace, func(time.Time) tea.Msg { return toolStatusShownMsg{} }),
				m.waitEvent(),
			)
		}
	case agent.EventToolEnd, agent.EventToolSkipped:
		m.finishTool(event)
		if event.Failed {
			m.status = safeText(event.Name) + " 执行失败，等待模型继续…"
		} else if !m.stopping {
			m.status = "工具执行完成，等待模型继续…"
		}
	}
	return m.waitEvent()
}

func (m *terminalModel) finishCompact(item delivery) tea.Cmd {
	m.busy, m.stopping = false, false
	m.cancel = nil
	m.pendingReady = nil
	body := ""
	switch {
	case errors.Is(item.err, context.Canceled):
		m.status = "已取消压缩"
		body = "已取消上下文压缩。"
	case item.err != nil:
		m.status = "压缩失败，可继续输入"
		body = "上下文压缩失败：" + item.err.Error()
	default:
		m.status = "空闲"
		body = item.text
	}
	m.pruneApprovals()
	focus := m.input.Focus()
	m.resizeInput()
	if body == "" {
		return tea.Batch(focus, m.waitEvent())
	}
	return tea.Batch(tea.Sequence(tea.Println(wrap(safeText(body), m.contentWidth())), focus), m.waitEvent())
}

// finishResponse 在完整响应结束后一次性提交正文。最终响应会先打印折叠摘要，
// 再打印最终回答，保证最终文本连续且位于摘要之后。
func (m *terminalModel) finishResponse(event Event) tea.Cmd {
	if flushed := m.markdown.Flush(); flushed != "" {
		m.responseOut = append(m.responseOut, flushed)
	}
	answer := strings.Join(m.responseOut, "\n\n")
	summary := ""
	if event.Final && len(m.liveTools) > 0 {
		for i := range m.liveTools {
			if m.liveTools[i].State == "running" {
				m.liveTools[i].State = "skipped"
			}
		}
		summary = m.toolSummaryText()
		m.lastTools = append([]toolRecord(nil), m.liveTools...)
		m.liveTools = nil
		m.expanded = false
	}
	m.resetResponse()
	if summary == "" && answer == "" {
		return m.waitEvent()
	}
	m.pendingPrint = &responsePrint{summary: summary, answer: answer}
	return tea.Tick(responseFlushDelay, func(time.Time) tea.Msg {
		return printResponseMsg{}
	})
}

// printResponse 先打印折叠摘要，再按行分批提交最终回答。长回答一次性渲染会给
// 终端带来明显停顿，分批可以更快显示摘要和开头内容。
func (m *terminalModel) printResponse(summary, answer string) tea.Cmd {
	prefix := ""
	if summary != "" {
		muted := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
		prefix = muted.Render(wrap(summary, m.contentWidth()))
		if answer != "" {
			prefix += "\n\n"
		}
	}
	var cmds []tea.Cmd
	if answer == "" {
		if prefix != "" {
			cmds = append(cmds, tea.Println(strings.TrimRight(prefix, "\n")))
		}
	} else {
		const chunkLines = 30
		lines := strings.Split(answer, "\n")
		first := true
		for start := 0; start < len(lines); start += chunkLines {
			end := min(len(lines), start+chunkLines)
			chunk := strings.Join(lines[start:end], "\n")
			if first {
				chunk = prefix + chunk
				first = false
			}
			cmds = append(cmds, tea.Println(chunk))
		}
	}
	if len(cmds) == 0 {
		return m.waitEvent()
	}
	cmds = append(cmds, m.waitEvent())
	return tea.Sequence(cmds...)
}

func (m *terminalModel) finishTurn(item delivery) tea.Cmd {
	// 异常中断可能没有经过正常响应提交；保留已经收到的正文。
	if flushed := m.markdown.Flush(); flushed != "" {
		m.responseOut = append(m.responseOut, flushed)
	}
	answer := strings.Join(m.responseOut, "\n\n")
	m.resetResponse()
	var body strings.Builder
	if answer != "" {
		body.WriteString(answer)
		body.WriteString("\n\n")
	}
	summary := ""
	if len(m.liveTools) > 0 {
		for i := range m.liveTools {
			if m.liveTools[i].State == "running" {
				m.liveTools[i].State = "skipped"
			}
		}
		summary = m.toolSummaryText()
		m.lastTools = append([]toolRecord(nil), m.liveTools...)
		m.liveTools = nil
		m.expanded = false
	}
	m.busy, m.stopping = false, false
	m.cancel = nil
	m.pendingReady = nil
	switch {
	case errors.Is(item.err, context.Canceled):
		m.status = "已取消，可继续输入"
		// 用户刚取消，不要立刻因为收件箱里已有的结果再次唤醒；结果在下次输入时一起送达。
		m.wakeBlocked = true
		appendBodyLine(&body, "已取消本轮；已完成的操作和上下文已保留。", m.contentWidth())
	case item.err != nil:
		m.status = "本轮停止，可继续输入"
		appendBodyLine(&body, "本轮停止："+item.err.Error(), m.contentWidth())
	default:
		m.status = "空闲"
	}
	if summary != "" {
		if body.Len() > 0 {
			body.WriteString("\n\n")
		} else {
			body.WriteString("\n")
		}
		body.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(wrap(summary, m.contentWidth())))
	}
	m.pruneApprovals()
	focus := m.input.Focus()
	m.resizeInput()
	if body.Len() > 0 {
		return tea.Batch(tea.Sequence(tea.Println(body.String()), focus), m.waitEvent())
	}
	return tea.Batch(focus, m.waitEvent())
}

func appendBodyLine(body *strings.Builder, text string, width int) {
	if body.Len() > 0 {
		body.WriteString("\n\n")
	}
	body.WriteString(wrap(safeText(text), width))
}

func toolIcon(state string) string {
	switch state {
	case "success":
		return "✓"
	case "failed":
		return "✗"
	case "skipped":
		return "○"
	default:
		return "…"
	}
}

func (m *terminalModel) beginTool(event Event) {
	m.liveTools = append(m.liveTools, toolRecord{
		ID: event.ID, Name: event.Name, Key: event.Key, State: "running",
	})
}

func (m *terminalModel) finishTool(event Event) {
	state := "success"
	if event.Failed {
		state = "failed"
	}
	if event.Kind == agent.EventToolSkipped {
		state = "skipped"
	}
	for i := len(m.liveTools) - 1; i >= 0; i-- {
		record := &m.liveTools[i]
		if (event.ID != "" && record.ID == event.ID) || (event.ID == "" && i == len(m.liveTools)-1) {
			if event.Name != "" {
				record.Name = event.Name
			}
			record.State = state
			record.Detail = event.Text
			return
		}
	}
	// 防御性处理：没有对应开始事件时也保留结果，避免丢失进度。
	m.liveTools = append(m.liveTools, toolRecord{
		ID: event.ID, Name: event.Name, Key: event.Key, State: state, Detail: event.Text,
	})
}

func (m *terminalModel) progressLines(width int) []string {
	if len(m.liveTools) == 0 {
		return nil
	}
	maxName := 0
	for _, record := range m.liveTools {
		maxName = max(maxName, ansi.StringWidth(safeText(record.Name)))
	}
	maxName = min(maxName, 10)
	lines := make([]string, 0, len(m.liveTools))
	for _, record := range m.liveTools {
		name := safeText(record.Name)
		if name == "" {
			name = "工具"
		}
		line := toolIcon(record.State) + " " + name +
			strings.Repeat(" ", max(0, maxName-ansi.StringWidth(name)))
		if record.Key != "" {
			line += "  " + safeText(record.Key)
		}
		lines = append(lines, ansi.Truncate(line, width, "…"))
	}
	return lines
}

func (m *terminalModel) toolSummaryText() string {
	if len(m.liveTools) == 0 {
		return ""
	}
	counts := make(map[string]int)
	var order []string
	problems := 0
	for _, record := range m.liveTools {
		name := safeText(record.Name)
		if name == "" {
			name = "工具"
		}
		if counts[name] == 0 {
			order = append(order, name)
		}
		counts[name]++
		if record.State == "failed" || record.State == "skipped" {
			problems++
		}
	}
	names := make([]string, 0, len(order))
	for _, name := range order {
		if counts[name] == 1 {
			names = append(names, name)
		} else {
			names = append(names, fmt.Sprintf("%s×%d", name, counts[name]))
		}
	}
	summary := fmt.Sprintf("▸ 已折叠 %d 次工具调用（%s）", len(m.liveTools), strings.Join(names, "、"))
	if problems > 0 {
		summary += fmt.Sprintf("，%d 次未成功", problems)
	}
	return summary + " · Ctrl+O 展开"
}

// expansionText 只在用户主动按 Ctrl+O 时用于写入 scrollback；默认折叠。
func (m *terminalModel) expansionText() string {
	if len(m.lastTools) == 0 {
		return ""
	}
	lines := []string{"", "── 最近一次工具过程 ──"}
	for _, record := range m.lastTools {
		name := safeText(record.Name)
		if name == "" {
			name = "工具"
		}
		line := toolIcon(record.State) + " " + name
		if record.Key != "" {
			line += "  " + safeText(record.Key)
		}
		lines = append(lines, line)
		detail := strings.TrimRight(safeText(record.Detail), "\n")
		if detail == "" {
			continue
		}
		for _, detailLine := range strings.Split(detail, "\n") {
			lines = append(lines, "  "+detailLine)
		}
	}
	return wrap(strings.Join(lines, "\n"), m.contentWidth())
}

func (m *terminalModel) approvalLines(width int) []string {
	state := m.approval
	if state == nil {
		return nil
	}
	line := func(label, value string) string {
		return ansi.Truncate(label+oneLine(value), width, "…")
	}
	lines := []string{"权限确认"}
	if origin := state.request.Origin; !origin.IsZero() {
		lines = append(lines, line("来源：", "子任务 "+origin.ID+" "+origin.Description))
	}
	lines = append(lines,
		line("工具：", state.request.Tool),
		line("目录：", state.request.WorkDir),
		line("原因：", state.request.Reason),
	)
	if state.request.Target != "" {
		lines = append(lines, line("目标：", state.request.Target))
	}
	if state.request.Detail != "" {
		lines = append(lines, line("详情：", state.request.Detail))
	}
	if state.request.CanRecord() {
		lines = append(lines, line("将记录：", state.request.RecordLabel()))
	}
	lines = append(lines, "")
	for index, option := range state.options {
		prefix := "  "
		if index == state.selected {
			prefix = "› "
		}
		lines = append(lines, ansi.Truncate(prefix+option.label, width, "…"))
	}
	if waiting := len(m.approvalQueue); waiting > 0 {
		lines = append(lines, fmt.Sprintf("另有 %d 个请求在等待", waiting))
	}
	cancelHint := "取消全部子任务"
	switch {
	case m.busy && len(m.subs) > 0:
		cancelHint = "取消本轮和子任务"
	case m.busy:
		cancelHint = "取消本轮"
	}
	lines = append(lines, "↑↓ 选择 · Enter 确认 · Ctrl+C "+cancelHint)
	for index, value := range lines {
		lines[index] = ansi.Truncate(value, width, "…")
	}
	return lines
}

func oneLine(text string) string {
	text = safeText(text)
	text = strings.ReplaceAll(text, "\n", " ")
	return strings.ReplaceAll(text, "\r", " ")
}

func (m *terminalModel) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	width := m.contentWidth()
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	if m.approval != nil {
		// 审批面板独占动态区域，无论主 Agent 是否忙碌。
		maxDynamic := max(1, m.height-1)
		lines := m.approvalLines(width)
		if len(lines) > maxDynamic {
			lines = lines[len(lines)-maxDynamic:]
		}
		return tea.NewView(strings.Join(lines, "\n"))
	}
	if m.busy {
		// 留一行给终端光标/滚动，避免动态区域刚好占满屏幕时把旧内容滚入历史。
		maxDynamic := max(1, m.height-1)
		reserve := 1
		if maxDynamic >= 2 {
			reserve = 2
		}
		available := max(0, maxDynamic-reserve)
		subs := m.subagentLines(width)
		if len(subs) > available {
			subs = subs[:available]
		}
		available -= len(subs)
		panel := m.progressLines(width)
		var parts []string
		if len(panel) > available {
			if available > 1 {
				keep := available - 1
				omitted := len(panel) - keep
				panel = panel[len(panel)-keep:]
				notice := fmt.Sprintf("… 更早 %d 条工具记录，结束后 Ctrl+O 查看", omitted)
				parts = append(parts, muted.Render(ansi.Truncate(notice, width, "…")))
			} else {
				panel = panel[len(panel)-available:]
			}
		}
		parts = append(parts, panel...)
		for _, line := range subs {
			parts = append(parts, muted.Render(line))
		}
		if len(parts) < maxDynamic {
			parts = append(parts, ansi.Truncate(m.status, width, "…"))
		}
		if len(parts) < maxDynamic {
			hint := "Ctrl+C 取消本轮"
			if len(m.subs) > 0 {
				hint = "Ctrl+C 取消本轮和子任务"
			}
			parts = append(parts, muted.Render(ansi.Truncate(hint, width, "…")))
		}
		return tea.NewView(strings.Join(parts, "\n"))
	}
	view := m.input.View()
	// textarea 本身最小宽度高于极窄终端，最后按显示宽度裁剪可见行。
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "")
	}
	border := muted.Render(strings.Repeat("─", width))
	hasBorder := m.height >= len(lines)+2
	menu := m.slashView(width, m.slashMaxRows(len(lines)))
	subs := m.subagentLines(width)
	if room := m.height - len(lines) - 3; len(subs) > max(0, room) {
		subs = subs[:max(0, room)]
	}
	var parts []string
	for _, line := range subs {
		parts = append(parts, muted.Render(line))
	}
	if len(menu) > 0 {
		parts = append(parts, menu...)
	}
	if hasBorder {
		parts = append(parts, border)
	}
	parts = append(parts, lines...)
	if hasBorder {
		parts = append(parts, border)
	}
	if m.height >= len(parts)+1 {
		hint := m.status
		if len(m.lastTools) > 0 && !m.expanded {
			hint += " · Ctrl+O 展开工具过程"
		}
		footer := hint + " · Enter 发送 · Alt+Enter 换行 · ↑↓ 历史 · /permissions 权限 · /exit 退出"
		parts = append(parts, muted.Render(ansi.Truncate(footer, width, "…")))
	}
	content := strings.Join(parts, "\n")
	result := tea.NewView(content)
	// 真实光标必须落在输入框中的文字位置：输入法组合文本、候选窗口会跟随
	// 这个位置。菜单、上边框占用行，需要加到输入框内的相对坐标上。
	if cursor := m.input.Cursor(); cursor != nil {
		if hasBorder {
			cursor.Position.Y++
		}
		cursor.Position.Y += len(menu) + len(subs)
		cursor.Position.X = min(cursor.Position.X, width-1)
		cursor.Position.Y = min(cursor.Position.Y, max(0, len(parts)-1))
		result.Cursor = cursor
	}
	return result
}
