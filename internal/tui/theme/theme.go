// Package theme configures the renderer before Bubble Tea's package init.
// Go initializes ready packages in import-path order; this project path sorts
// before github.com/charmbracelet/bubbletea. Keep this dependency free of tea.
package theme

import "github.com/charmbracelet/lipgloss"

func init() {
	// v1 的 tea.init 会主动查询终端背景并读取终端回复，即使 CLI 只输出帮助
	// 或处理管道。界面使用终端原色，固定此项可避免启动阶段的终端读写。
	lipgloss.SetHasDarkBackground(true)
}
