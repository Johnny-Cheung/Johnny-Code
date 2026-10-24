package tui

import (
	"fmt"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const maxSlashRows = 8

// SlashCommand 是斜杠菜单中的一项。Command 不带前导斜杠，
// 例如 hello-file、skills reload 或 exit。
type SlashCommand struct {
	Command     string
	Description string
	Source      string
	Builtin     bool
}

type slashMenuState struct {
	visible   bool
	dismissed bool
	filtered  []SlashCommand
	selected  int
	offset    int
}

func slashContext(value string) bool {
	if value == "" || !strings.HasPrefix(value, "/") {
		return false
	}
	if strings.ContainsAny(value, "\r\n") {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsSpace)
}

// refreshSlashMenu 根据当前输入重建过滤结果。只在一行、以 / 开头且尚未
// 输入空格时展示；Esc 关闭后保持关闭，直到输入离开斜杠上下文。
func (m *terminalModel) refreshSlashMenu() {
	value := m.input.Value()
	if !slashContext(value) {
		if strings.HasPrefix(m.status, "已补全 ") {
			m.status = "空闲"
		}
		m.slash = slashMenuState{}
		return
	}
	if m.busy || m.approval != nil || m.options.SlashCommands == nil {
		m.slash.visible = false
		m.slash.filtered = nil
		return
	}
	if m.slash.dismissed {
		m.slash.visible = false
		m.slash.filtered = nil
		return
	}
	if m.slashEntryRows() <= 0 {
		m.slash.visible = false
		m.slash.filtered = nil
		return
	}
	previous, hadPrevious := m.slash.current()
	entries := m.options.SlashCommands()
	query := strings.ToLower(strings.TrimPrefix(value, "/"))
	filtered := make([]SlashCommand, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(strings.ToLower(entry.Command), query) {
			filtered = append(filtered, entry)
		}
	}
	if len(filtered) == 0 {
		m.slash.filtered = nil
		m.slash.visible = false
		m.slash.selected = 0
		m.slash.offset = 0
		return
	}
	selected := 0
	if hadPrevious {
		for index, entry := range filtered {
			if entry.Command == previous.Command && entry.Builtin == previous.Builtin {
				selected = index
				break
			}
		}
	}
	m.slash.filtered = filtered
	m.slash.selected = selected
	m.slash.visible = true
	m.slash.clamp(m.slashEntryRows())
}

func (m *terminalModel) moveSlash(delta int) {
	if !m.slash.visible || len(m.slash.filtered) == 0 {
		return
	}
	m.slash.selected = (m.slash.selected + delta + len(m.slash.filtered)) % len(m.slash.filtered)
	m.slash.clamp(m.slashEntryRows())
}

func (m *terminalModel) completeSlash() {
	entry, ok := m.slash.current()
	if !ok {
		return
	}
	m.input.SetValue("/" + entry.Command + " ")
	m.input.CursorEnd()
	m.resizeInput()
	m.slash = slashMenuState{}
	m.status = fmt.Sprintf("已补全 /%s · 可输入参数后 Enter 执行", entry.Command)
}

func (m *terminalModel) dismissSlash() {
	m.slash.dismissed = true
	m.slash.visible = false
	m.slash.filtered = nil
}

func (m *terminalModel) slashExactMatch() bool {
	entry, ok := m.slash.current()
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(m.input.Value()), "/"+entry.Command)
}

func (m *terminalModel) slashEntryRows() int {
	rows := m.slashMaxRows(max(1, m.input.LineCount()))
	if rows < 2 {
		return 0
	}
	return rows - 1
}

func (m *terminalModel) slashMaxRows(inputLines int) int {
	if m.height <= 0 {
		return 0
	}
	// 为输入框上下边框、footer 和一行安全余量预留空间。
	available := m.height - max(1, inputLines) - 4
	if available < 2 {
		return 0
	}
	return min(maxSlashRows, available)
}

func (m *terminalModel) slashView(width, maxRows int) []string {
	if !m.slash.visible || len(m.slash.filtered) == 0 || maxRows < 2 {
		return nil
	}
	entryRows := maxRows - 1
	m.slash.clamp(entryRows)
	end := min(len(m.slash.filtered), m.slash.offset+entryRows)

	maxCommand := 0
	for index := m.slash.offset; index < end; index++ {
		maxCommand = max(maxCommand, ansi.StringWidth("/"+m.slash.filtered[index].Command))
	}
	maxCommand = min(maxCommand, 26)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	selectedStyle := lipgloss.NewStyle().Bold(true)

	lines := make([]string, 0, maxRows)
	for index := m.slash.offset; index < end; index++ {
		entry := m.slash.filtered[index]
		marker := "  "
		if index == m.slash.selected {
			marker = "› "
		}
		command := "/" + entry.Command
		padding := strings.Repeat(" ", max(0, maxCommand-ansi.StringWidth(command)))
		line := marker + command + padding + "  "
		if entry.Source != "" {
			line += "[" + safeText(entry.Source) + "] "
		}
		line += safeText(entry.Description)
		line = ansi.Truncate(line, width, "…")
		if index == m.slash.selected {
			line = selectedStyle.Render(line)
		}
		lines = append(lines, line)
	}
	footer := fmt.Sprintf("↑↓ 选择（%d/%d） · Enter 补全 · Esc 关闭", m.slash.selected+1, len(m.slash.filtered))
	lines = append(lines, muted.Render(ansi.Truncate(footer, width, "…")))
	return lines
}

func (s *slashMenuState) current() (SlashCommand, bool) {
	if s == nil || s.selected < 0 || s.selected >= len(s.filtered) {
		return SlashCommand{}, false
	}
	return s.filtered[s.selected], true
}

func (s *slashMenuState) clamp(entryRows int) {
	if entryRows <= 0 || len(s.filtered) == 0 {
		s.selected = 0
		s.offset = 0
		return
	}
	s.selected = min(max(0, s.selected), len(s.filtered)-1)
	if s.selected < s.offset {
		s.offset = s.selected
	}
	if s.selected >= s.offset+entryRows {
		s.offset = s.selected - entryRows + 1
	}
	s.offset = min(max(0, s.offset), max(0, len(s.filtered)-entryRows))
}
