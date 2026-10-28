package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// areaHeightLimit 是任务区最多占用的行数：终端高度的三分之一，且不超过 room。
func (m *terminalModel) areaHeightLimit(room int) int {
	return min(m.height/3, max(0, room))
}

// taskAreaLines 返回任务区要显示的行，最多 limit 行。放不下时按优先级保留：
// 标题行、当前步骤及其运行中的任务、单个子任务、未开始的步骤，最后是已完成的步骤，
// 先折叠成一行再丢弃；终端太矮时只显示一行摘要。
func (m *terminalModel) taskAreaLines(width, limit int) []string {
	if limit <= 0 || !m.hasTasks() {
		return nil
	}
	entries := m.areaEntries()
	if len(entries) > limit {
		entries = collapseDone(entries)
	}
	if len(entries) > limit && limit < 3 {
		return []string{ansi.Truncate(m.areaSummary(), width, "…")}
	}
	if len(entries) > limit {
		entries = keepByRank(entries, limit)
	}
	lines := make([]string, len(entries))
	for i, entry := range entries {
		lines[i] = renderAreaEntry(entry, width)
	}
	return lines
}

// renderAreaEntry 先截断主体、保留行尾状态：标题很长或终端很窄时，用户最先需要看到的
// 是这一步做到哪了、这个任务还要多久以及是否在等审批。
func renderAreaEntry(entry areaEntry, width int) string {
	tail := ansi.Truncate(entry.tail, max(0, width/2), "…")
	room := max(0, width-len(tail))
	text := ansi.Truncate(entry.text, room, "…")
	return text + tail
}

// collapseDone 把已完成的步骤折叠成一行。
func collapseDone(entries []areaEntry) []areaEntry {
	done := 0
	for _, entry := range entries {
		if entry.done {
			done++
		}
	}
	if done < 2 {
		return entries
	}
	collapsed := make([]areaEntry, 0, len(entries)-done+1)
	placed := false
	for _, entry := range entries {
		if !entry.done {
			collapsed = append(collapsed, entry)
			continue
		}
		if !placed {
			collapsed = append(collapsed, areaEntry{rank: rankDone, text: fmt.Sprintf("✓ 已完成 %d 步", done), done: true})
			placed = true
		}
	}
	return collapsed
}

// keepByRank 保留优先级最高的 limit-1 行并按原顺序显示，最后一行说明省略了多少行。
func keepByRank(entries []areaEntry, limit int) []areaEntry {
	order := make([]int, len(entries))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return entries[order[a]].rank < entries[order[b]].rank })
	keep := make(map[int]bool, limit)
	for _, index := range order[:limit-1] {
		keep[index] = true
	}
	kept := make([]areaEntry, 0, limit)
	for i, entry := range entries {
		if keep[i] {
			kept = append(kept, entry)
		}
	}
	return append(kept, areaEntry{rank: rankDone, text: fmt.Sprintf("… 另有 %d 行未显示", len(entries)-len(kept))})
}

func stepCountsText(step WorkflowStepNotice) string {
	if step.Partial == 0 && step.Failed == 0 {
		return "全部完成"
	}
	var parts []string
	if step.Completed > 0 {
		parts = append(parts, fmt.Sprintf("%d 个完成", step.Completed))
	}
	if step.Partial > 0 {
		parts = append(parts, fmt.Sprintf("%d 个只完成了一部分", step.Partial))
	}
	if step.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d 个失败", step.Failed))
	}
	return strings.Join(parts, "，")
}

// stepEndLine 是一步结束时打印的一行汇总。
func stepEndLine(w *WorkflowNotice, no int) string {
	if w == nil || no < 1 || no > len(w.Steps) {
		return ""
	}
	step := w.Steps[no-1]
	mark := "✓"
	if step.Partial > 0 || step.Failed > 0 {
		mark = "△"
	}
	return fmt.Sprintf("%s workflow 第 %d/%d 步「%s」已结束：%d 个任务，%s", mark, no, len(w.Steps), oneLine(step.Title), step.Total, stepCountsText(step))
}

// workflowEndLine 是工作流结束时打印的一行汇总。
func workflowEndLine(w *WorkflowNotice) string {
	if w == nil {
		return ""
	}
	elapsed := formatElapsed(time.Since(w.Started))
	label := fmt.Sprintf("workflow「%s」", oneLine(w.Title))
	done, total := w.doneSteps(), len(w.Steps)
	switch w.Status {
	case workflowCompleted:
		return fmt.Sprintf("✓ %s已完成：%d 步全部结束（%s）", label, total, elapsed)
	case workflowEnded:
		line := fmt.Sprintf("○ %s已提前结束：完成 %d/%d 步，其余未执行（%s）", label, done, total, elapsed)
		if reason := oneLine(w.Reason); reason != "" {
			line += "，原因：" + reason
		}
		return line
	default:
		return fmt.Sprintf("○ %s已取消：完成 %d/%d 步，其余未执行（%s）", label, done, total, elapsed)
	}
}
