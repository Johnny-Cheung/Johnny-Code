package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// SessionSummary 是 /resume 列表中的一行。
type SessionSummary struct {
	ID         string
	Title      string
	WorkDir    string
	Model      string
	Turns      int
	Modified   time.Time
	Incomplete bool
	Locked     bool
	Current    bool
}

// Resumable 表示这一行能否被选择。当前会话和使用中的会话不可以。
func (s SessionSummary) Resumable() bool {
	return !s.Current && !s.Locked
}

// ResumeRequest 是用户选择会话后从 TUI 返回的信号；调用方据此重新启动到目标会话。
type ResumeRequest struct {
	ID string
}

func (r *ResumeRequest) Error() string {
	return "恢复会话：" + r.ID
}

type resumeMenu struct {
	items    []SessionSummary
	selected int
}

func (m *terminalModel) openResume(prompt string) tea.Cmd {
	if m.options.Sessions == nil {
		return m.showLocalResult(prompt, "当前入口不支持恢复会话；请在终端界面使用 /resume。")
	}
	items, err := m.options.Sessions()
	if err != nil {
		return m.showLocalResult(prompt, "读取会话列表失败："+err.Error())
	}
	if len(items) == 0 {
		return m.showLocalResult(prompt, "当前目录没有可恢复的会话。")
	}
	selected := -1
	for index, item := range items {
		if item.Resumable() {
			selected = index
			break
		}
	}
	if selected < 0 {
		return m.showLocalResult(prompt, "没有可恢复的会话：当前会话不能重复恢复，使用中的会话需要先关闭。")
	}
	m.input.Reset()
	m.input.Blur()
	m.slash = slashMenuState{}
	m.history = append(m.history, prompt)
	m.historyIndex, m.draft = len(m.history), ""
	m.resizeInput()
	m.resume = &resumeMenu{items: items, selected: selected}
	m.status = "选择要恢复的会话"
	return nil
}

func (m *terminalModel) handleResumeKey(keys string) tea.Cmd {
	state := m.resume
	if state == nil {
		return nil
	}
	switch keys {
	case "up", "k":
		m.moveResume(-1)
	case "down", "j", "tab":
		m.moveResume(1)
	case "esc", "ctrl+c":
		m.resume = nil
		m.status = "空闲"
		m.input.Focus()
		m.resizeInput()
	case "enter":
		item := state.items[state.selected]
		if !item.Resumable() {
			if item.Current {
				m.status = "当前会话已经打开，不能重复恢复"
			} else {
				m.status = "该会话正在被其他进程使用"
			}
			return nil
		}
		m.resumeID = item.ID
		m.resume = nil
		return tea.Quit
	}
	return nil
}

func (m *terminalModel) moveResume(delta int) {
	state := m.resume
	if state == nil || len(state.items) == 0 {
		return
	}
	for step := 1; step <= len(state.items); step++ {
		index := (state.selected + delta*step + len(state.items)) % len(state.items)
		if state.items[index].Resumable() {
			state.selected = index
			return
		}
	}
}

func (m *terminalModel) resumeLines(width, maxRows int) []string {
	state := m.resume
	if state == nil {
		return nil
	}
	header := "恢复会话"
	dir := "目录：" + m.options.WorkDir
	hint := "↑↓ 选择 · Enter 恢复 · Esc 取消"
	lines := []string{ansi.Truncate(header, width, "…")}

	// 标题、目录、提示和可能的未完成说明各占一行，其余留给列表。
	reserved := 3
	if state.items[state.selected].Incomplete {
		reserved++
	}
	rowLimit := max(1, maxRows-reserved)
	start := 0
	if state.selected >= rowLimit {
		start = state.selected - rowLimit + 1
	}
	end := min(len(state.items), start+rowLimit)
	if start > 0 {
		lines = append(lines, ansi.Truncate(fmt.Sprintf("… 更早 %d 个会话", start), width, "…"))
	}
	for index := start; index < end; index++ {
		item := state.items[index]
		marker := "  "
		if index == state.selected {
			marker = "› "
		}
		title := oneLine(item.Title)
		if title == "" {
			title = "（无标题）"
		}
		row := fmt.Sprintf("%s%s  %d 轮  %s", marker, item.Modified.Format("01-02 15:04"), item.Turns, title)
		switch {
		case item.Current:
			row += "  [当前会话]"
		case item.Locked:
			row += "  [使用中]"
		case item.Incomplete:
			row += "  [未完成]"
		}
		lines = append(lines, ansi.Truncate(row, width, "…"))
	}
	if end < len(state.items) {
		lines = append(lines, ansi.Truncate(fmt.Sprintf("… 还有 %d 个会话", len(state.items)-end), width, "…"))
	}
	lines = append(lines, ansi.Truncate(dir, width, "…"))
	if state.items[state.selected].Incomplete {
		lines = append(lines, ansi.Truncate("上次未正常结束：将恢复到最后一个完整轮次", width, "…"))
	}
	lines = append(lines, ansi.Truncate(hint, width, "…"))
	if len(lines) > maxRows {
		lines = lines[len(lines)-maxRows:]
	}
	return lines
}
