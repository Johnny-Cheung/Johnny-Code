package subagent

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

// scriptedChildren 让每个子任务停在原地，直到测试按任务标签放行并指定报告内容。
// 报告以 ERROR: 开头时模拟模型请求失败。
type scriptedChildren struct {
	mu    sync.Mutex
	gates map[string]chan string
}

func newScriptedChildren(h *harness) *scriptedChildren {
	s := &scriptedChildren{gates: make(map[string]chan string)}
	h.respond = func(ctx context.Context, req model.Request) (model.Response, error) {
		if !isChild(req) {
			return textResponse("好"), nil
		}
		label := taskLabel(requestTexts(req))
		select {
		case text := <-s.gate(label):
			if reason, failed := strings.CutPrefix(text, "ERROR:"); failed {
				return model.Response{}, errors.New(reason)
			}
			return textResponse(text), nil
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
	}
	return s
}

func (s *scriptedChildren) gate(label string) chan string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gates[label] == nil {
		s.gates[label] = make(chan string, 1)
	}
	return s.gates[label]
}

func (s *scriptedChildren) finish(label, text string) { s.gate(label) <- text }

// taskLabel 取出 freshReadRequest 写进提示词的任务标签（最后一个“任务：”后的第一个词；fork 的指令头里也有“任务：”）。
func taskLabel(texts []string) string {
	joined := strings.Join(texts, " ")
	index := strings.LastIndex(joined, "任务：")
	if index < 0 {
		return ""
	}
	fields := strings.Fields(joined[index+len("任务："):])
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func outline(titles ...string) []tool.WorkflowStep {
	steps := make([]tool.WorkflowStep, len(titles))
	for i, title := range titles {
		steps[i] = tool.WorkflowStep{Title: title, Goal: "完成" + title}
	}
	return steps
}

func (h *harness) startWorkflow(titles ...string) {
	h.t.Helper()
	if _, err := h.manager.StartWorkflow("测试工作流", outline(titles...)); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) runStep(step int, labels ...string) (string, error) {
	requests := make([]tool.SpawnRequest, len(labels))
	for i, label := range labels {
		requests[i] = freshReadRequest(label)
	}
	return h.manager.RunWorkflowStep(context.Background(), step, requests)
}

func (h *harness) requireWorkflowStatus(want WorkflowStatus) {
	h.t.Helper()
	h.manager.mu.Lock()
	got := WorkflowStatus("")
	if h.manager.wf != nil {
		got = h.manager.wf.status
	}
	h.manager.mu.Unlock()
	if got != want {
		h.t.Fatalf("工作流状态应为 %s：%s", want, got)
	}
}

// deliver 像空闲唤醒那样让主会话处理收件箱，返回送达的文本和来源。
func (h *harness) deliver() (string, []string) {
	h.t.Helper()
	h.mu.Lock()
	before := len(h.requests)
	h.mu.Unlock()
	var sources []string
	if _, err := h.parent.RunInbox(context.Background(), func(event agent.Event) error {
		if event.Type == agent.EventStart {
			sources = event.Sources
		}
		return nil
	}); err != nil {
		h.t.Fatalf("唤醒失败：%v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, req := range h.requests[before:] {
		if !isChild(req) {
			last := req.Messages[len(req.Messages)-1]
			return last.Content[0].Text, sources
		}
	}
	h.t.Fatal("主会话没有发出请求")
	return "", nil
}

// enqueueBatch 在测试里直接走管理器内部的整批登记路径（生产入口是 workflow 的 run 动作）。
func (h *harness) enqueueBatch(requests []tool.SpawnRequest) ([]string, error) {
	h.manager.mu.Lock()
	if err := h.manager.readyLocked(); err != nil {
		h.manager.mu.Unlock()
		return nil, err
	}
	batch, err := h.manager.enqueueBatchLocked(requests, nil)
	if err != nil {
		h.manager.mu.Unlock()
		return nil, err
	}
	ids := make([]string, len(batch))
	for i, t := range batch {
		ids[i] = t.info.ID
	}
	started := h.manager.admitLocked()
	h.manager.mu.Unlock()
	h.manager.launch(started)
	return ids, nil
}
