package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/charmbracelet/x/ansi"
)

// 子任务通知的类型。
const (
	SubagentStart = "start"
	SubagentState = "state"
	SubagentEnd   = "end"
)

// maxWakeRounds 是同一次用户输入之后连续自动唤醒主 Agent 的上限，防止主 Agent
// 反复派出子任务、反复被唤醒。
const maxWakeRounds = 5

// SubagentNotice 是子任务状态变化的有界展示数据，不含子任务的过程细节。
type SubagentNotice struct {
	Kind        string
	ID          string
	Description string
	Started     time.Time
	Waiting     bool
	// 以下字段仅 SubagentEnd 有值。
	Status   agent.Status
	Error    string
	Partial  bool
	Duration time.Duration
}

// SubagentControl 由 CLI 实现，连接后台子任务管理器和主会话。
type SubagentControl interface {
	Running() int
	CancelAll() int
	Wait()
	// PendingResults 返回等待送回主对话的子任务结果数。
	PendingResults() int
	// Subscribe 注册状态通知，返回取消注册的函数。回调可能来自任意 goroutine。
	Subscribe(func(SubagentNotice)) func()
}

type subagentMsg struct{ notice SubagentNotice }

type subagentTickMsg struct{}

type subagentView struct {
	id          string
	description string
	started     time.Time
	waiting     bool
}

func (m *terminalModel) applySubagent(notice SubagentNotice) tea.Cmd {
	switch notice.Kind {
	case SubagentStart:
		m.subs = append(m.subs, subagentView{id: notice.ID, description: notice.Description, started: notice.Started})
		return m.scheduleSubagentTick()
	case SubagentState:
		for i := range m.subs {
			if m.subs[i].id == notice.ID {
				m.subs[i].waiting = notice.Waiting
			}
		}
	case SubagentEnd:
		for i := range m.subs {
			if m.subs[i].id == notice.ID {
				m.subs = append(m.subs[:i], m.subs[i+1:]...)
				break
			}
		}
		m.pruneApprovals()
		muted := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
		return tea.Println(muted.Render(wrap(subagentEndLine(notice), m.contentWidth())))
	}
	return nil
}

func (m *terminalModel) scheduleSubagentTick() tea.Cmd {
	if m.subTick || len(m.subs) == 0 {
		return nil
	}
	m.subTick = true
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return subagentTickMsg{} })
}

// subagentEndLine 是子任务结束时打印的一行提示。
func subagentEndLine(notice SubagentNotice) string {
	label := fmt.Sprintf("子任务 %s %s", safeText(notice.ID), oneLine(notice.Description))
	elapsed := formatElapsed(notice.Duration)
	switch {
	case notice.Status == agent.StatusCanceled:
		return fmt.Sprintf("○ %s 已取消", label)
	case notice.Status == agent.StatusCompleted:
		return fmt.Sprintf("✓ %s 已完成（%s）", label, elapsed)
	case notice.Partial:
		return fmt.Sprintf("△ %s 只完成了一部分：%s（%s）", label, partialReason(notice.Status), elapsed)
	default:
		reason := oneLine(notice.Error)
		if reason == "" {
			reason = string(notice.Status)
		}
		if runes := []rune(reason); len(runes) > 120 {
			reason = string(runes[:120]) + "…"
		}
		return fmt.Sprintf("✗ %s 失败：%s（%s）", label, reason, elapsed)
	}
}

func partialReason(status agent.Status) string {
	switch status {
	case agent.StatusMaxTurns:
		return "达到模型请求次数上限"
	case agent.StatusMaxTokens:
		return "输出达到上限"
	default:
		return "达到总时长上限"
	}
}

func formatElapsed(d time.Duration) string {
	seconds := int(d.Round(time.Second) / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%ds", max(0, seconds))
	}
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
}

// subagentLines 是常驻子任务区：每个运行中的子任务一行。
func (m *terminalModel) subagentLines(width int) []string {
	lines := make([]string, 0, len(m.subs))
	for _, sub := range m.subs {
		state := "运行中"
		if sub.waiting {
			state = "等待审批"
		}
		line := fmt.Sprintf("◆ %s %s · %s · %s", safeText(sub.id), oneLine(sub.description), formatElapsed(time.Since(sub.started)), state)
		lines = append(lines, ansi.Truncate(line, width, "…"))
	}
	return lines
}

// cancelSubagents 取消所有运行中的子任务并返回数量。
func (m *terminalModel) cancelSubagents() int {
	if m.options.Subagents == nil {
		return 0
	}
	return m.options.Subagents.CancelAll()
}

// exit 退出界面。仍有子任务运行时先提示、取消并等它们停止，再退出。
func (m *terminalModel) exit() tea.Cmd {
	m.quitting = true
	control := m.options.Subagents
	if control == nil || control.Running() == 0 {
		return tea.Quit
	}
	count := control.Running()
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	return tea.Sequence(
		tea.Println(muted.Render(fmt.Sprintf("%d 个子任务将被取消，等待它们停止…", count))),
		stopSubagentsAndQuit(control),
	)
}

func stopSubagentsAndQuit(control SubagentControl) tea.Cmd {
	return func() tea.Msg {
		control.CancelAll()
		control.Wait()
		return tea.Quit()
	}
}

// maybeWake 在主 Agent 空闲、输入框为空且有子任务结果等待时自动唤醒主 Agent 汇总。
// 用户正在输入时不抢占，结果留在收件箱，等下一次请求前注入。
func (m *terminalModel) maybeWake() tea.Cmd {
	control := m.options.Subagents
	if control == nil || m.options.RunWake == nil || m.busy || m.quitting || m.approval != nil || m.wakeBlocked {
		return nil
	}
	if m.input.Value() != "" || control.PendingResults() == 0 {
		return nil
	}
	if m.wakeRounds >= maxWakeRounds {
		m.wakeBlocked = true
		muted := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
		return tea.Println(muted.Render(wrap(fmt.Sprintf("子任务结果已连续唤醒 %d 次，不再自动唤醒；结果会在你下次输入时一起送达。", maxWakeRounds), m.contentWidth())))
	}
	m.wakeRounds++
	m.beginTurnState("子任务完成，主 Agent 正在汇总…")
	run := m.options.RunWake
	m.launchTurn(func(ctx context.Context, emit func(Event) error, notice func(string)) (agent.Result, error) {
		return run(ctx, emit, notice)
	})
	return nil
}
