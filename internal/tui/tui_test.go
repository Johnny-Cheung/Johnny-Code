package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

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
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("分析下面代码：\nfunc main() {}"), Paste: true})
	if m.busy || m.input.Value() != "分析下面代码：\nfunc main() {}" {
		t.Fatalf("粘贴不得发送或丢失换行：%q", m.input.Value())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if m.input.Value() != "分析下面代码：\nfunc main() {}\n" {
		t.Fatalf("Alt+Enter 应换行：%q", m.input.Value())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.input.Value() != "" || m.quitting || m.busy {
		t.Fatal("空闲 Ctrl+C 只能清空输入")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.busy {
		t.Fatal("空输入不能发送")
	}
}

func TestInputHistoryRestoresDraftAndNavigatesMultiline(t *testing.T) {
	m := testModel(t)
	m.history = []string{"first", "second\nline"}
	m.historyIndex = len(m.history)
	m.input.SetValue("draft")
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.input.Value() != "second\nline" {
		t.Fatal(m.input.Value())
	}
	// 多行历史先移动到首行，再按上键才切换到更早任务。
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.input.Value() != "second\nline" {
		t.Fatal("多行光标移动不应切换历史")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.input.Value() != "first" {
		t.Fatal(m.input.Value())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.input.Value() != "draft" {
		t.Fatalf("应恢复未发送草稿：%q", m.input.Value())
	}
}

func TestExitKeys(t *testing.T) {
	for _, value := range []string{"", " /exit "} {
		m := testModel(t)
		m.input.SetValue(value)
		key := tea.KeyMsg{Type: tea.KeyCtrlD}
		if value != "" {
			key.Type = tea.KeyEnter
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
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
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
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	<-started
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("discard"), Paste: true})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
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
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
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

func TestStreamLinesPreserveAllTextAndBoundTail(t *testing.T) {
	m := testModel(t)
	m.width = 11
	var printed strings.Builder
	input := "第一行中文\n\n" + strings.Repeat("长文本", 100) + "\nend"
	for _, r := range input {
		m.tail += safeText(string(r))
		printed.WriteString(m.splitTail(false))
		if ansi.StringWidth(m.tail) > m.contentWidth() {
			t.Fatal("流式尾行没有界限")
		}
	}
	printed.WriteString(m.splitTail(true))
	got := strings.ReplaceAll(printed.String(), "\n", "")
	if got != strings.ReplaceAll(input, "\n", "") {
		t.Fatal("长文本内容丢失或重复")
	}
	if !strings.Contains(printed.String(), "第一行中文\n\n") {
		t.Fatal("空行丢失")
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
		view := m.View()
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

func TestOutputFenceAcknowledgesActualWrite(t *testing.T) {
	var buffer bytes.Buffer
	output := &checkedOutput{Writer: &buffer, cancel: func() {}}
	marker, ack := output.fence()
	select {
	case <-ack:
		t.Fatal("写出之前不能确认")
	default:
	}
	input := "→ bash echo hello" + marker + "\r\n"
	if n, err := output.Write([]byte(input)); err != nil || n != len(input) {
		t.Fatalf("写入失败：%d %v", n, err)
	}
	if err := <-ack; err != nil {
		t.Fatal(err)
	}
	if buffer.String() != "→ bash echo hello\r\n" {
		t.Fatalf("内部标记不得泄露：%q", buffer.String())
	}
}

type shortOutput struct{}

func (shortOutput) Write(data []byte) (int, error) { return 0, nil }

func TestOutputFailureCancelsAndRejectsFence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &checkedOutput{Writer: shortOutput{}, cancel: cancel}
	marker, ack := output.fence()
	_, err := output.Write([]byte("tool" + marker))
	if !errors.Is(err, io.ErrShortWrite) || !errors.Is(<-ack, io.ErrShortWrite) || ctx.Err() == nil {
		t.Fatalf("短写未传播：%v", err)
	}
	_, next := output.fence()
	if !errors.Is(<-next, io.ErrShortWrite) {
		t.Fatal("后续工具应收到原始写错误")
	}
}

func TestToolStartMustBeWrittenBeforeExecution(t *testing.T) {
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
	select {
	case <-executed:
		t.Fatal("显示前已执行")
	default:
	}
	if item.marker == "" {
		t.Fatal("缺少实际写入屏障")
	}
	if _, err := m.output.Write([]byte("tool" + item.marker)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executed:
	case <-time.After(time.Second):
		t.Fatal("写入成功后未继续")
	}
	<-m.events
}
