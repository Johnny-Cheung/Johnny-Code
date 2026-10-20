// Package tui provides an inline terminal interface over the existing Agent.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	marker string
	done   bool
	result agent.Result
	err    error
}

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
	tail         string
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
	input.KeyMap.Paste.SetEnabled(false) // 终端括号粘贴由 KeyRunes 接收，不访问系统剪贴板。
	// 使用终端自身的前景/背景，避免自适应颜色探测等待 OSC 回复并占用输入。
	style := textarea.Style{
		Base: lipgloss.NewStyle(), Text: lipgloss.NewStyle(),
		CursorLine: lipgloss.NewStyle(), EndOfBuffer: lipgloss.NewStyle(),
		Prompt:      lipgloss.NewStyle().Bold(true),
		Placeholder: lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
	}
	input.FocusedStyle, input.BlurredStyle = style, style
	// 静态光标避免组件的异步 blink 闭包访问随后被替换的输入状态。
	input.Cursor.SetMode(cursor.CursorStatic)
	input.Focus()
	return &terminalModel{options: options, ctx: ctx, output: output, events: make(chan delivery),
		workers: workers, input: input, width: 80, height: 24, status: "空闲"}
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
		m.resizeInput()
		lines := m.splitTail(false)
		if lines != "" {
			return m, tea.Println(strings.TrimSuffix(lines, "\n"))
		}
		return m, nil
	case delivery:
		return m, m.receive(msg)
	case tea.KeyMsg:
		if m.busy {
			if msg.Type == tea.KeyCtrlC && !m.stopping {
				m.stopping = true
				m.status = "正在取消，等待当前操作停止…"
				m.cancel()
			}
			return m, nil
		}
		if msg.Type == tea.KeyRunes && msg.Paste {
			text := strings.ReplaceAll(string(msg.Runes), "\r\n", "\n")
			text = strings.ReplaceAll(text, "\r", "\n")
			// textarea 库有 10000 行硬上限；整批拒绝，避免静默截掉任务尾部。
			if m.input.LineCount()+strings.Count(text, "\n") > 10000 {
				m.status = "输入最多 10000 行，本次粘贴未插入"
				return m, nil
			}
			msg.Runes = []rune(text)
			message = msg
		}
		if !msg.Paste {
			if ((msg.Type == tea.KeyEnter && msg.Alt) || msg.Type == tea.KeyCtrlJ) && m.input.LineCount() >= 10000 {
				m.status = "输入最多 10000 行"
				return m, nil
			}
			switch {
			case msg.Type == tea.KeyCtrlC:
				m.input.Reset()
				m.historyIndex = len(m.history)
				m.draft = ""
				m.resizeInput()
				return m, nil
			case msg.Type == tea.KeyCtrlD && m.input.Value() == "":
				m.quitting = true
				return m, tea.Quit
			case msg.Type == tea.KeyEnter && !msg.Alt:
				prompt := strings.TrimSpace(m.input.Value())
				if prompt == "" {
					return m, nil
				}
				if prompt == "/exit" {
					m.quitting = true
					return m, tea.Quit
				}
				return m, m.start(prompt)
			case msg.Type == tea.KeyUp && m.input.Line() == 0:
				m.navigateHistory(-1)
				return m, nil
			case msg.Type == tea.KeyDown && m.input.Line() == m.input.LineCount()-1:
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
			var ack <-chan error
			if event.Kind == agent.EventToolStart {
				item.marker, ack = m.output.fence()
			}
			// 使用会话 context，允许取消后的工具结果和收尾事件继续显示。
			select {
			case m.events <- item:
			case <-m.ctx.Done():
				return m.ctx.Err()
			}
			if ack != nil {
				select {
				case err := <-ack:
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
	var text string
	if item.done {
		text = m.splitTail(true)
		m.busy, m.stopping = false, false
		m.cancel = nil
		switch {
		case errors.Is(item.err, context.Canceled):
			m.status = "已取消，可继续输入"
			text += "\n已取消本轮；已完成的操作和上下文已保留。\n"
		case item.err != nil:
			m.status = "本轮停止，可继续输入"
			text += "\n本轮停止：" + safeText(item.err.Error()) + "\n"
		default:
			m.status = "空闲"
		}
		focus := m.input.Focus()
		if text != "" {
			return tea.Sequence(tea.Println(wrap(strings.TrimSuffix(text, "\n"), m.contentWidth())), focus)
		}
		return focus
	}
	if item.notice != "" {
		text = m.splitTail(true) + safeText(strings.TrimSuffix(item.notice, "\n"))
	} else {
		event := item.event
		switch event.Kind {
		case agent.EventRequestStart:
			text = m.splitTail(true)
			if !m.stopping {
				m.status = "正在等待模型回复…"
			}
		case agent.EventModel:
			if event.Text != "" {
				if !m.stopping {
					m.status = "正在接收回复…"
				}
				m.tail += safeText(event.Text)
				text = m.splitTail(false)
			}
		case agent.EventResponse, agent.EventEnd:
			text = m.splitTail(true)
		case agent.EventToolStart:
			text = m.splitTail(true) + "→ " + safeText(event.Name) + "  " + safeText(event.Key)
			if !m.stopping {
				m.status = "正在执行 " + safeText(event.Name) + "…"
			}
		case agent.EventToolEnd, agent.EventToolSkipped:
			status := "✓ 成功"
			if event.Failed {
				status = "✗ 失败"
			}
			if m.stopping || event.Kind == agent.EventToolSkipped {
				status = "○ 已中止"
			}
			text = m.splitTail(true) + "  " + status
			if event.Text != "" {
				text += "\n" + safeText(event.Text)
			}
		}
	}
	if text != "" || item.marker != "" {
		return tea.Sequence(tea.Println(wrap(strings.TrimSuffix(text, "\n"), m.contentWidth())+item.marker), m.waitEvent())
	}
	return m.waitEvent()
}

// splitTail 把完整行（包括超长单行的完整显示行）提交 scrollback，动态 View
// 最多持有一行尾部；无需复制整段回复，也不会被 renderer 按屏幕高度裁掉。
func (m *terminalModel) splitTail(flush bool) string {
	m.tail = wrap(m.tail, m.contentWidth())
	if flush {
		text := m.tail
		m.tail = ""
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		return text
	}
	index := strings.LastIndexByte(m.tail, '\n')
	if index < 0 {
		return ""
	}
	text := m.tail[:index+1]
	m.tail = m.tail[index+1:]
	return text
}

func (m *terminalModel) View() string {
	if m.quitting {
		return ""
	}
	width := m.contentWidth()
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	if m.busy {
		parts := []string{}
		if m.tail != "" && m.height > 1 {
			parts = append(parts, m.tail)
		}
		parts = append(parts, ansi.Truncate(m.status, width, "…"))
		if m.height > len(parts) {
			parts = append(parts, muted.Render(ansi.Truncate("Ctrl+C 取消本轮", width, "…")))
		}
		return strings.Join(parts, "\n")
	}
	view := m.input.View()
	// textarea 本身最小宽度高于极窄终端，最后按显示宽度裁剪可见行。
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "")
	}
	view = strings.Join(lines, "\n")
	parts := []string{view}
	if m.height >= len(lines)+2 {
		parts = append([]string{muted.Render(strings.Repeat("─", width))}, parts...)
		parts = append(parts, muted.Render(ansi.Truncate(m.status+" · Enter 发送 · Alt+Enter 换行 · ↑↓ 历史 · /exit 退出", width, "…")))
	}
	return strings.Join(parts, "\n")
}
