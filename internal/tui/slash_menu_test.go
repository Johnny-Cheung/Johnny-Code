package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func slashTestModel(t *testing.T) *terminalModel {
	t.Helper()
	m := testModel(t)
	m.options.SlashCommands = func() []SlashCommand {
		return []SlashCommand{
			{Command: "hello-file", Description: "生成问候文件。", Source: "项目"},
			{Command: "report-maker", Description: "生成报告。", Source: "用户"},
			{Command: "skills", Description: "查看 Skill。", Source: "内置", Builtin: true},
			{Command: "exit", Description: "退出。", Source: "内置", Builtin: true},
		}
	}
	return m
}

func TestSlashMenuFiltersAndCompletes(t *testing.T) {
	m := slashTestModel(t)
	m.input.SetValue("/")
	m.input.CursorEnd()
	m.refreshSlashMenu()
	if !m.slash.visible || len(m.slash.filtered) != 4 {
		t.Fatalf("输入 / 应展示全部命令：visible=%v filtered=%v", m.slash.visible, m.slash.filtered)
	}

	m.input.SetValue("/he")
	m.refreshSlashMenu()
	if !m.slash.visible || len(m.slash.filtered) != 1 || m.slash.filtered[0].Command != "hello-file" {
		t.Fatalf("前缀过滤错误：%+v", m.slash.filtered)
	}
	m.Update(keyPress(tea.KeyEnter))
	if m.input.Value() != "/hello-file " {
		t.Fatalf("Enter 应补全而不是立即执行：%q", m.input.Value())
	}
	if m.slash.visible || m.busy {
		t.Fatalf("补全后应关闭菜单并保持空闲：visible=%v busy=%v", m.slash.visible, m.busy)
	}
	if !strings.Contains(m.status, "已补全 /hello-file") {
		t.Fatalf("补全状态缺失：%q", m.status)
	}
	m.refreshSlashMenu()
	if m.status != "空闲" {
		t.Fatalf("离开斜杠上下文后应清除补全状态：%q", m.status)
	}
}

func TestSlashMenuNavigationAndRendering(t *testing.T) {
	m := slashTestModel(t)
	m.history = []string{"history"}
	m.historyIndex = 1
	m.input.SetValue("/")
	m.refreshSlashMenu()

	m.Update(keyPress(tea.KeyDown))
	if m.slash.selected != 1 {
		t.Fatalf("↓ 应移动菜单选择：%d", m.slash.selected)
	}
	if m.input.Value() != "/" {
		t.Fatalf("菜单导航不应修改输入或历史：%q", m.input.Value())
	}
	m.Update(keyPress(tea.KeyUp))
	if m.slash.selected != 0 {
		t.Fatalf("↑ 应移动菜单选择：%d", m.slash.selected)
	}

	view := strings.Join(m.slashView(100, 8), "\n")
	for _, want := range []string{"hello-file", "report-maker", "项目", "用户", "内置", "Enter 补全", "Esc 关闭"} {
		if !strings.Contains(view, want) {
			t.Fatalf("菜单缺少 %q：%s", want, view)
		}
	}
}

func TestSlashMenuEscDismissesUntilContextResets(t *testing.T) {
	m := slashTestModel(t)
	m.input.SetValue("/he")
	m.refreshSlashMenu()
	m.Update(keyPress(tea.KeyEsc))
	if m.slash.visible {
		t.Fatal("Esc 应关闭菜单")
	}
	m.input.SetValue("/hel")
	m.refreshSlashMenu()
	if m.slash.visible {
		t.Fatal("Esc 关闭后继续输入不应重新打开菜单")
	}
	m.input.Reset()
	m.refreshSlashMenu()
	m.input.SetValue("/")
	m.refreshSlashMenu()
	if !m.slash.visible {
		t.Fatal("清空输入后重新输入 / 应重新打开菜单")
	}
}

func TestSlashMenuExactCommandFallsThrough(t *testing.T) {
	m := slashTestModel(t)
	handled := ""
	m.options.LocalCommand = func(line string) (LocalCommandResult, bool) {
		handled = line
		return LocalCommandResult{Output: "列表"}, true
	}
	m.input.SetValue("/skills")
	m.refreshSlashMenu()
	m.Update(keyPress(tea.KeyEnter))
	if handled != "/skills" {
		t.Fatalf("完整命令应按 Enter 直接执行：%q", handled)
	}
	if m.input.Value() != "" || m.busy {
		t.Fatalf("本地命令后应清空输入并保持空闲：%q busy=%v", m.input.Value(), m.busy)
	}
}
