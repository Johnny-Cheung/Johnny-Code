package tui

import (
	"fmt"
	"strings"
	"time"
)

// 工作流通知类型。任务自身的启动、状态和结束沿用 SubagentStart、SubagentState 和 SubagentEnd，
// 但属于工作流的任务（Step > 0）不单独出现在任务区的单任务行里，也不逐个打印结束提示。
const (
	// SubagentWorkflow 表示工作流登记或步骤启动，只更新状态。
	SubagentWorkflow = "workflow"
	// SubagentStepEnd 表示一步全部结束，Step 是步骤序号。
	SubagentStepEnd = "step_end"
	// SubagentWorkflowEnd 表示工作流结束（完成、提前结束或取消）。
	SubagentWorkflowEnd = "workflow_end"
)

// 工作流和步骤的状态字符串，与 subagent 包一致。
const (
	workflowActive    = "active"
	workflowCompleted = "completed"
	workflowEnded     = "ended_early"
	workflowCanceled  = "canceled"

	stepPending = "pending"
	stepRunning = "running"
	stepDone    = "done"
)

// WorkflowTaskNotice 是当前步骤里一个运行中或等待审批的任务。
type WorkflowTaskNotice struct {
	ID          string
	Description string
	Started     time.Time
	Waiting     bool
}

// WorkflowStepNotice 是大纲里一步的当前状态。
type WorkflowStepNotice struct {
	No        int
	Title     string
	State     string
	Total     int
	Finished  int
	Completed int
	Partial   int
	Failed    int
	Active    []WorkflowTaskNotice
	Queued    int
}

// WorkflowNotice 是工作流的完整快照。Version 递增，界面丢弃比已收到的更旧的快照。
type WorkflowNotice struct {
	Version uint64
	Title   string
	Status  string
	Reason  string
	Started time.Time
	Steps   []WorkflowStepNotice
}

func (w *WorkflowNotice) doneSteps() int {
	done := 0
	for _, step := range w.Steps {
		if step.State == stepDone {
			done++
		}
	}
	return done
}

// current 返回正在进行的步骤；没有进行中的步骤时返回下一个待启动的步骤。
func (w *WorkflowNotice) current() int {
	for i, step := range w.Steps {
		if step.State == stepRunning {
			return i
		}
	}
	for i, step := range w.Steps {
		if step.State == stepPending {
			return i
		}
	}
	return len(w.Steps) - 1
}

// applyWorkflowSnapshot 保存较新的快照，乱序到达的旧快照和已结束工作流的快照被丢弃。
func (m *terminalModel) applyWorkflowSnapshot(w *WorkflowNotice) {
	if w == nil || w.Status != workflowActive || w.Version <= m.workflowFloor {
		return
	}
	if m.workflow != nil && w.Version < m.workflow.Version {
		return
	}
	m.workflow = w
}

func (m *terminalModel) hasTasks() bool { return len(m.subs) > 0 || m.workflow != nil }

type areaEntry struct {
	rank int
	text string
	done bool
}

const (
	rankHeader = iota
	rankCurrent
	rankTask
	rankPending
	rankDone
)

func taskLine(task WorkflowTaskNotice) string {
	state := "运行中"
	if task.Waiting {
		state = "等待审批"
	}
	return fmt.Sprintf("    ◆ %s %s · %s · %s", safeText(task.ID), oneLine(task.Description), formatElapsed(time.Since(task.Started)), state)
}

func stepLine(step WorkflowStepNotice) string {
	mark := "○"
	switch step.State {
	case stepDone:
		mark = "✓"
	case stepRunning:
		mark = "◆"
	}
	line := fmt.Sprintf("%s %d %s", mark, step.No, oneLine(step.Title))
	if step.State == stepRunning {
		line += fmt.Sprintf("    %d/%d 个任务完成", step.Finished, step.Total)
		if step.Queued > 0 {
			line += fmt.Sprintf(" · %d 个排队", step.Queued)
		}
	}
	return line
}

// areaEntries 按显示顺序列出任务区的所有行，每行带保留优先级。
func (m *terminalModel) areaEntries() []areaEntry {
	var entries []areaEntry
	if w := m.workflow; w != nil {
		cur := w.current()
		entries = append(entries, areaEntry{rank: rankHeader, text: fmt.Sprintf("workflow %s  %d/%d", oneLine(w.Title), cur+1, len(w.Steps))})
		for i, step := range w.Steps {
			rank := rankPending
			switch {
			case i == cur:
				rank = rankCurrent
			case step.State == stepDone:
				rank = rankDone
			}
			entries = append(entries, areaEntry{rank: rank, text: stepLine(step), done: step.State == stepDone})
			if i == cur {
				for _, task := range step.Active {
					entries = append(entries, areaEntry{rank: rankTask, text: taskLine(task)})
				}
			}
		}
	}
	for _, sub := range m.subs {
		state := "运行中"
		if sub.waiting {
			state = "等待审批"
		}
		line := fmt.Sprintf("◆ %s %s · %s · %s", safeText(sub.id), oneLine(sub.description), formatElapsed(time.Since(sub.started)), state)
		entries = append(entries, areaEntry{rank: rankTask, text: line})
	}
	return entries
}

// areaSummary 是终端太矮时的一行摘要。
func (m *terminalModel) areaSummary() string {
	var parts []string
	waiting := 0
	if w := m.workflow; w != nil {
		step := w.Steps[w.current()]
		parts = append(parts, fmt.Sprintf("workflow %d/%d", step.No, len(w.Steps)))
		if step.State == stepRunning {
			parts = append(parts, fmt.Sprintf("%d/%d 个任务", step.Finished, step.Total))
		}
		for _, task := range step.Active {
			if task.Waiting {
				waiting++
			}
		}
	}
	if len(m.subs) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个子任务", len(m.subs)))
		for _, sub := range m.subs {
			if sub.waiting {
				waiting++
			}
		}
	}
	if waiting > 0 {
		parts = append(parts, fmt.Sprintf("%d 个等待审批", waiting))
	}
	return strings.Join(parts, " · ")
}
