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

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/charmbracelet/x/ansi"
)

// Event 只携带有界展示数据；完整响应和续接数据仍由 Agent 和诊断记录持有。
type Event struct {
	Kind   agent.EventType
	Text   string
	Name   string
	Key    string
	Failed bool
}

type Options struct {
	Input   io.Reader
	Output  io.Writer
	Model   string
	WorkDir string
	Version string
	RunTurn func(context.Context, string, func(Event) error, func(string)) (agent.Result, error)
}

type delivery struct {
	event  Event
	notice string
	ready  chan error
	done   bool
	result agent.Result
	err    error
}

// toolStatusShownMsg 标记状态区已经获得一次渲染机会，可以检查输出错误并
// 放行工具执行。
type toolStatusShownMsg struct{}

const toolStatusGrace = 20 * time.Millisecond

type terminalModel struct {
	options      Options
	ctx          context.Context
	output       *checkedOutput
	events       chan delivery
	workers      *sync.WaitGroup
	input        textarea.Model
	width        int
	height       int
	busy         bool
	stopping     bool
	quitting     bool
	cancel       context.CancelFunc
	status       string
	markdown     markdownStream
	pendingReady chan error
	history      []string
	historyIndex int
	draft        string
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
	program := tea.NewProgram(m, tea.WithInput(input), tea.WithOutput(output),
		tea.WithContext(runCtx), tea.WithoutSignalHandler())
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

func (m *terminalModel) Init() tea.Cmd {
	header := fmt.Sprintf("Johnny-Code · v%s\n模型：%s\n目录：%s\n", safeText(m.options.Version), safeText(m.options.Model), safeText(m.options.WorkDir))
	return tea.Println(wrap(header, m.contentWidth()))
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
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		m.markdown.SetWidth(m.contentWidth())
		m.resizeInput()
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
	case tea.PasteMsg:
		if m.busy {
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
		if m.busy {
			if keys == "ctrl+c" && !m.stopping {
				m.stopping = true
				m.status = "正在取消，等待当前操作停止…"
				m.cancel()
			}
			return m, nil
		}
		switch keys {
		case "ctrl+c":
			m.input.Reset()
			m.historyIndex = len(m.history)
			m.draft = ""
			m.resizeInput()
			return m, nil
		case "ctrl+d":
			if m.input.Value() == "" {
				m.quitting = true
				return m, tea.Quit
			}
		case "enter":
			prompt := strings.TrimSpace(m.input.Value())
			if prompt == "" {
				return m, nil
			}
			if prompt == "/exit" {
				m.quitting = true
				return m, tea.Quit
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
}

func (m *terminalModel) start(prompt string) tea.Cmd {
	m.busy, m.stopping = true, false
	m.status = "正在等待模型回复…"
	m.pendingReady = nil
	m.markdown.Reset()
	m.input.Reset()
	m.input.Blur()
	m.history = append(m.history, prompt)
	m.historyIndex, m.draft = len(m.history), ""
	m.resizeInput()
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
		result, err := m.options.RunTurn(turnCtx, prompt, emit, notice)
		select {
		case m.events <- delivery{done: true, result: result, err: err}:
		case <-m.ctx.Done():
		}
	}()
	return tea.Sequence(tea.Println(wrap("\n❯ "+safeText(prompt)+"\n", m.contentWidth())), m.waitEvent())
}

func (m *terminalModel) receive(item delivery) tea.Cmd {
	var body strings.Builder
	var afterStatus tea.Cmd
	last := ""
	appendText := func(text, kind string) {
		text = strings.TrimRight(text, "\n")
		if text == "" || (kind == "markdown" && strings.TrimSpace(ansi.Strip(text)) == "") {
			return
		}
		if body.Len() > 0 {
			if kind == "tool_result" && last == "tool_start" {
				body.WriteString("\n")
			} else {
				body.WriteString("\n\n")
			}
		}
		body.WriteString(text)
		last = kind
	}
	appendMarkdown := func(rendered string) { appendText(rendered, "markdown") }
	appendPlain := func(text, kind string) {
		appendText(wrap(safeText(text), m.contentWidth()), kind)
	}

	if item.done {
		appendMarkdown(m.markdown.Flush())
		m.busy, m.stopping = false, false
		m.cancel = nil
		m.pendingReady = nil
		switch {
		case errors.Is(item.err, context.Canceled):
			m.status = "已取消，可继续输入"
			appendPlain("已取消本轮；已完成的操作和上下文已保留。", "status")
		case item.err != nil:
			m.status = "本轮停止，可继续输入"
			appendPlain("本轮停止："+item.err.Error(), "status")
		default:
			m.status = "空闲"
		}
		focus := m.input.Focus()
		m.resizeInput()
		if body.Len() > 0 {
			return tea.Sequence(tea.Println(body.String()), focus)
		}
		return focus
	}
	if item.notice != "" {
		appendMarkdown(m.markdown.Flush())
		appendPlain(strings.TrimRight(item.notice, "\n"), "notice")
	} else {
		event := item.event
		switch event.Kind {
		case agent.EventRequestStart:
			appendMarkdown(m.markdown.Flush())
			if !m.stopping {
				m.status = "正在等待模型回复…"
			}
		case agent.EventModel:
			if event.Text != "" {
				if !m.stopping {
					m.status = "正在接收回复…"
				}
				appendMarkdown(m.markdown.Write(event.Text))
			}
		case agent.EventResponse, agent.EventEnd:
			appendMarkdown(m.markdown.Flush())
		case agent.EventToolStart:
			// 简洁模式不把工具开始、结果和预览写入滚动历史；状态区显示
			// 当前工具。先给状态区一个渲染周期并检查输出错误，再放行工具，
			// 避免终端不可用时仍然产生副作用。
			if !m.stopping {
				m.status = "正在执行 " + safeText(event.Name) + "…"
			}
			if item.ready != nil {
				m.pendingReady = item.ready
				afterStatus = tea.Tick(toolStatusGrace, func(time.Time) tea.Msg {
					return toolStatusShownMsg{}
				})
			}
		case agent.EventToolEnd, agent.EventToolSkipped:
			if event.Failed {
				m.status = safeText(event.Name) + " 执行失败，等待模型继续…"
			} else if !m.stopping {
				m.status = "工具执行完成，等待模型继续…"
			}
		}
	}
	var cmds []tea.Cmd
	if body.Len() > 0 {
		output := body.String()
		// 单独提交的 Markdown 块末尾留一空行；后续正文或状态可能在下一次
		// 有序交付中打印，空行需要在本次打印时补上。
		if last == "markdown" {
			output += "\n"
		}
		cmds = append(cmds, tea.Println(output))
	}
	if afterStatus != nil {
		cmds = append(cmds, afterStatus)
	}
	if len(cmds) == 0 {
		return m.waitEvent()
	}
	cmds = append(cmds, m.waitEvent())
	return tea.Sequence(cmds...)
}

func (m *terminalModel) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	width := m.contentWidth()
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	if m.busy {
		reserve := 1
		if m.height >= 3 {
			reserve = 2
		}
		maxPreview := max(0, m.height-reserve)
		var parts []string
		if preview := strings.TrimRight(m.markdown.Preview(), "\n"); preview != "" && maxPreview > 0 {
			parts = strings.Split(wrap(preview, width), "\n")
			if len(parts) > maxPreview {
				parts = parts[len(parts)-maxPreview:]
			}
			for i, line := range parts {
				parts[i] = ansi.Truncate(line, width, "…")
			}
		}
		if len(parts) < m.height {
			parts = append(parts, ansi.Truncate(m.status, width, "…"))
		}
		if len(parts) < m.height {
			parts = append(parts, muted.Render(ansi.Truncate("Ctrl+C 取消本轮", width, "…")))
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
	var parts []string
	if hasBorder {
		parts = append(parts, border)
	}
	parts = append(parts, lines...)
	if hasBorder {
		parts = append(parts, border)
	}
	if m.height >= len(parts)+1 {
		footer := m.status + " · Enter 发送 · Alt+Enter 换行 · ↑↓ 历史 · /exit 退出"
		parts = append(parts, muted.Render(ansi.Truncate(footer, width, "…")))
	}
	content := strings.Join(parts, "\n")
	result := tea.NewView(content)
	// 真实光标必须落在输入框中的文字位置：输入法组合文本、候选窗口会跟随
	// 这个位置。上边框占用一行，需要加到输入框内的相对坐标上。
	if cursor := m.input.Cursor(); cursor != nil {
		if hasBorder {
			cursor.Position.Y++
		}
		cursor.Position.X = min(cursor.Position.X, width-1)
		cursor.Position.Y = min(cursor.Position.Y, max(0, len(parts)-1))
		result.Cursor = cursor
	}
	return result
}
