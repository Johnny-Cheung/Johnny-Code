package subagent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

// RunWorkflowStep 启动工作流的一步：整批校验，通过则全部登记，能跑的先跑，其余排队。
// 步骤必须按顺序启动，上一步结束前不能启动下一步。
func (m *Manager) RunWorkflowStep(_ context.Context, step int, requests []tool.SpawnRequest) (string, error) {
	if len(requests) == 0 || len(requests) > tool.MaxWorkflowTasks {
		return "", fmt.Errorf("每步需要 1 到 %d 个任务，现在有 %d 个", tool.MaxWorkflowTasks, len(requests))
	}
	for _, request := range requests {
		if err := m.validateRequest(request); err != nil {
			return "", err
		}
	}
	m.mu.Lock()
	if err := m.readyLocked(); err != nil {
		m.mu.Unlock()
		return "", err
	}
	w := m.wf
	if w == nil || w.status != WorkflowActive {
		err := m.noActiveWorkflowLocked()
		m.mu.Unlock()
		return "", err
	}
	if err := w.checkStart(step); err != nil {
		m.mu.Unlock()
		return "", err
	}
	s := w.steps[step-1]
	s.total, s.finished, s.counts = len(requests), 0, StepCounts{}
	s.reportBytes = min(MaxReportBytes, StepReportBudget/len(requests))
	s.reports = make([]string, len(requests))
	batch, err := m.enqueueBatchLocked(requests, s)
	if err != nil {
		s.total, s.reportBytes, s.reports = 0, 0, nil
		m.mu.Unlock()
		return "", err
	}
	s.state, s.started = StepRunning, time.Now()
	started := m.admitLocked()
	queued := 0
	labels := make([]string, 0, len(batch))
	for _, t := range batch {
		if t.info.State == StateQueued {
			queued++
		}
		labels = append(labels, t.info.ID+" "+t.info.Description)
	}
	title := s.title
	m.mu.Unlock()
	m.emit(Event{Type: EventStepStarted, Step: step})
	m.launch(started)
	return fmt.Sprintf("第 %d 步「%s」已启动：%d 个任务（%d 个在运行，%d 个排队）：%s。它们在后台并行运行，全部结束后所有报告会一次送回；不需要等待或轮询，请结束本轮或继续其他工作。",
		step, title, len(batch), len(batch)-queued, queued, strings.Join(labels, "；")), nil
}

// checkStart 检查步骤能否启动：序号有效、没有进行中的步骤、严格按顺序。
func (w *workflow) checkStart(step int) error {
	if step < 1 || step > len(w.steps) {
		return fmt.Errorf("步骤序号 %d 无效，大纲共 %d 步", step, len(w.steps))
	}
	next := 1
	for _, s := range w.steps {
		if s.state == StepRunning {
			return fmt.Errorf("第 %d 步「%s」还没有结束，等它的报告送回后再启动第 %d 步", s.no, s.title, step)
		}
		if s.state == StepDone {
			next = s.no + 1
		}
	}
	if step < next {
		return fmt.Errorf("第 %d 步已经完成，不能重复启动；下一步是第 %d 步「%s」", step, next, w.steps[next-1].title)
	}
	if step > next {
		return fmt.Errorf("步骤必须按顺序启动，下一步是第 %d 步「%s」", next, w.steps[next-1].title)
	}
	return nil
}

// stepOutcome 是一步全部结束时要送回主 Agent 的内容。
type stepOutcome struct {
	no      int
	message string
	view    *WorkflowView
}

// stepTaskDone 记录工作流任务的结果。这一步最后一个任务结束时返回整步消息，否则返回 nil。
func (m *Manager) stepTaskDone(t *task, status agent.Status, report string) *stepOutcome {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, s := m.wf, t.step
	if w == nil || w != t.wf || w.status != WorkflowActive || s.state != StepRunning {
		return nil
	}
	s.reports[t.slot] = report
	s.finished++
	switch {
	case status == agent.StatusCompleted:
		s.counts.Completed++
	case IsPartial(status):
		s.counts.Partial++
	default:
		s.counts.Failed++
	}
	if s.finished < s.total {
		return nil
	}
	s.state = StepDone
	if s.no == len(w.steps) {
		w.status = WorkflowCompleted
	}
	return &stepOutcome{no: s.no, message: stepMessage(w, s), view: m.workflowViewLocked()}
}

// stepMessage 把一步的所有报告和工作流进度拼成一条交给主 Agent 的消息。
// 报告按任务在这一步里的顺序排列，各自带数据声明；标题和进度由运行时生成。
func stepMessage(w *workflow, s *wfStep) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[workflow「%s」第 %d/%d 步「%s」已结束：%d 个任务，%s]\n\n", w.title, s.no, len(w.steps), s.title, s.total, countsText(s.counts))
	for _, report := range s.reports {
		b.WriteString(report)
		b.WriteString("\n\n")
	}
	b.WriteString(progressText(w))
	return b.String()
}

func countsText(counts StepCounts) string {
	if counts.Partial == 0 && counts.Failed == 0 {
		return "全部完成"
	}
	var parts []string
	if counts.Completed > 0 {
		parts = append(parts, fmt.Sprintf("%d 个完成", counts.Completed))
	}
	if counts.Partial > 0 {
		parts = append(parts, fmt.Sprintf("%d 个只完成了一部分", counts.Partial))
	}
	if counts.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d 个失败", counts.Failed))
	}
	return strings.Join(parts, "，")
}

// progressText 写明每一步的状态和下一步该做什么，使计划不依赖主 Agent 的记忆，也不会被上下文压缩丢掉。
func progressText(w *workflow) string {
	marks := make([]string, 0, len(w.steps))
	var next *wfStep
	for _, s := range w.steps {
		mark := "○"
		if s.state == StepDone {
			mark = "✓"
		} else if next == nil {
			next = s
		}
		marks = append(marks, fmt.Sprintf("%s %d %s", mark, s.no, s.title))
	}
	text := "[工作流进度] " + strings.Join(marks, " · ") + "\n"
	if next == nil {
		return text + fmt.Sprintf("所有 %d 步都已结束，工作流完成。请根据以上报告向用户汇总结果。", len(w.steps))
	}
	return text + fmt.Sprintf("下一步：第 %d 步「%s」，目标：%s。请根据以上报告调用 workflow（action=run, step=%d）给出这一步的任务；计划不合适时用 action=end 提前结束。", next.no, next.title, next.goal, next.no)
}
