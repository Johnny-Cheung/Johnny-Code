package subagent

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

// StepReportBudget 是一步内所有任务报告正文的总量上限；任务多时每份报告相应变短。
const StepReportBudget = 32 << 10

type WorkflowStatus string

const (
	WorkflowActive     WorkflowStatus = "active"
	WorkflowCompleted  WorkflowStatus = "completed"
	WorkflowEndedEarly WorkflowStatus = "ended_early"
	WorkflowCanceled   WorkflowStatus = "canceled"
)

type StepState string

const (
	StepPending StepState = "pending"
	StepRunning StepState = "running"
	StepDone    StepState = "done"
	// StepSkipped 表示工作流结束时这一步没有完成（包括被取消的进行中的步骤）。
	StepSkipped StepState = "skipped"
)

// StepCounts 统计一步内已结束任务的结果。
type StepCounts struct{ Completed, Partial, Failed int }

type wfStep struct {
	no          int
	title, goal string
	state       StepState
	total       int
	finished    int
	counts      StepCounts
	reportBytes int
	reports     []string
	started     time.Time
}

type workflow struct {
	title   string
	steps   []*wfStep
	status  WorkflowStatus
	reason  string
	started time.Time
}

// StepView 是一步的只读状态。Active 只含运行中和等待审批的任务，排队中的只计数。
type StepView struct {
	No       int
	Title    string
	Goal     string
	State    StepState
	Total    int
	Finished int
	Counts   StepCounts
	Active   []Info
	Queued   int
}

// WorkflowView 是工作流的只读快照。Version 随每次快照递增，界面据此丢弃乱序到达的旧快照。
type WorkflowView struct {
	Version uint64
	Title   string
	Status  WorkflowStatus
	Reason  string
	Started time.Time
	Steps   []StepView
}

// Done 返回已完成的步骤数。
func (v WorkflowView) Done() int {
	done := 0
	for _, step := range v.Steps {
		if step.State == StepDone {
			done++
		}
	}
	return done
}

func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }

// workflowViewLocked 生成当前工作流的快照，没有工作流时返回 nil。
func (m *Manager) workflowViewLocked() *WorkflowView {
	w := m.wf
	if w == nil {
		return nil
	}
	m.wfVersion++
	view := &WorkflowView{Version: m.wfVersion, Title: w.title, Status: w.status, Reason: w.reason, Started: w.started}
	for _, s := range w.steps {
		view.Steps = append(view.Steps, StepView{No: s.no, Title: s.title, Goal: s.goal, State: s.state, Total: s.total, Finished: s.finished, Counts: s.counts})
	}
	for _, t := range m.tasks {
		if t.step != nil && t.wf == w {
			step := &view.Steps[t.step.no-1]
			step.Active = append(step.Active, t.info)
		}
	}
	for _, t := range m.queue {
		if t.step != nil && t.wf == w {
			view.Steps[t.step.no-1].Queued++
		}
	}
	for i := range view.Steps {
		active := view.Steps[i].Active
		sort.Slice(active, func(a, b int) bool { return active[a].Started.Before(active[b].Started) })
	}
	return view
}

// WorkflowActive 报告是否有进行中的工作流。
func (m *Manager) WorkflowActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.wf != nil && m.wf.status == WorkflowActive
}

// Workflow 返回进行中工作流的当前快照。
func (m *Manager) Workflow() (WorkflowView, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wf == nil || m.wf.status != WorkflowActive {
		return WorkflowView{}, false
	}
	return *m.workflowViewLocked(), true
}

// noActiveWorkflowLocked 说明为什么现在不能操作工作流，让模型知道下一步该做什么。
func (m *Manager) noActiveWorkflowLocked() error {
	w := m.wf
	switch {
	case w == nil:
		return errors.New("还没有登记工作流，请先调用 workflow（action=start）声明大纲")
	case w.status == WorkflowCompleted:
		return fmt.Errorf("工作流「%s」已经全部完成；需要新的工作流请重新 start", w.title)
	case w.status == WorkflowEndedEarly:
		return fmt.Errorf("工作流「%s」已经提前结束；需要新的工作流请重新 start", w.title)
	case w.status == WorkflowCanceled:
		return fmt.Errorf("工作流「%s」已被用户取消，不要继续启动步骤；请向用户确认下一步怎么做", w.title)
	}
	return nil
}

// StartWorkflow 登记大纲。同一时间只允许一个进行中的工作流。
func (m *Manager) StartWorkflow(title string, steps []tool.WorkflowStep) (string, error) {
	title = oneLine(title)
	if title == "" {
		return "", errors.New("工作流缺少标题")
	}
	if len(steps) == 0 || len(steps) > tool.MaxWorkflowSteps {
		return "", fmt.Errorf("工作流的大纲需要 1 到 %d 步", tool.MaxWorkflowSteps)
	}
	m.mu.Lock()
	if err := m.readyLocked(); err != nil {
		m.mu.Unlock()
		return "", err
	}
	if m.wf != nil && m.wf.status == WorkflowActive {
		current := m.wf.title
		m.mu.Unlock()
		return "", fmt.Errorf("已有进行中的工作流「%s」；请先让它完成，或用 action=end 提前结束后再开始新的", current)
	}
	w := &workflow{title: title, status: WorkflowActive, started: time.Now()}
	outline := make([]string, 0, len(steps))
	for i, item := range steps {
		w.steps = append(w.steps, &wfStep{no: i + 1, title: oneLine(item.Title), goal: oneLine(item.Goal), state: StepPending})
		outline = append(outline, fmt.Sprintf("%d %s", i+1, oneLine(item.Title)))
	}
	m.wf = w
	view := m.workflowViewLocked()
	m.mu.Unlock()
	m.emit(Event{Type: EventWorkflowStarted, Workflow: view})
	return fmt.Sprintf("工作流「%s」已登记，共 %d 步：%s。现在调用 workflow（action=run, step=1）给出第 1 步的任务细节并启动；每次只启动一步，这一步全部结束后报告会一次送回，再启动下一步。",
		title, len(steps), strings.Join(outline, "；")), nil
}

// EndWorkflow 由主 Agent 提前结束工作流：取消本工作流运行中和排队的任务，剩余步骤标为未执行。
func (m *Manager) EndWorkflow(reason string) (string, error) {
	m.mu.Lock()
	if m.wf == nil || m.wf.status != WorkflowActive {
		err := m.noActiveWorkflowLocked()
		m.mu.Unlock()
		return "", err
	}
	title := m.wf.title
	m.cancelWorkflowTasksLocked()
	view, skipped := m.finishWorkflowLocked(WorkflowEndedEarly, oneLine(reason))
	m.mu.Unlock()
	m.emit(Event{Type: EventWorkflowEnded, Workflow: view})
	text := fmt.Sprintf("工作流「%s」已提前结束，运行中的任务已取消。", title)
	if len(skipped) > 0 {
		text += "没有执行的步骤：" + joinInts(skipped) + "。"
	}
	return text, nil
}

// cancelWorkflowTasksLocked 取消当前工作流的运行中任务并丢弃排队的任务；单个委派的任务不受影响。
func (m *Manager) cancelWorkflowTasksLocked() {
	for _, t := range m.tasks {
		if t.step != nil {
			t.cancel()
		}
	}
	kept := make([]*task, 0, len(m.queue))
	for _, t := range m.queue {
		if t.step != nil {
			m.discardLocked(t)
			continue
		}
		kept = append(kept, t)
	}
	m.queue = kept
}

// finishWorkflowLocked 把进行中的工作流标为结束状态，没完成的步骤标为未执行。
// 返回结束时的快照和未执行的步骤序号；没有进行中的工作流时返回 nil。
func (m *Manager) finishWorkflowLocked(status WorkflowStatus, reason string) (*WorkflowView, []int) {
	w := m.wf
	if w == nil || w.status != WorkflowActive {
		return nil, nil
	}
	w.status, w.reason = status, reason
	var skipped []int
	for _, s := range w.steps {
		if s.state != StepDone {
			s.state = StepSkipped
			skipped = append(skipped, s.no)
		}
	}
	return m.workflowViewLocked(), skipped
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = fmt.Sprintf("第 %d 步", value)
	}
	return strings.Join(parts, "、")
}
