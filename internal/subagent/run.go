package subagent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func (m *Manager) emit(event Event) {
	if m.cfg.OnEvent != nil {
		m.cfg.OnEvent(event)
	}
}

// run 在后台执行一个子任务。结束时先释放文件范围，再把结果放进父会话的收件箱，
// 最后才发出结束事件，使界面收到事件时收件箱已经可以被唤醒轮读取。
func (m *Manager) run(t *task) {
	defer m.wg.Done()
	limitCtx, cancelLimit := context.WithTimeout(tool.WithOrigin(t.ctx, tool.Origin{ID: t.info.ID, Description: t.info.Description}), m.cfg.TimeLimit)
	defer cancelLimit()
	var sink EventSink
	if m.cfg.OpenLog != nil {
		sink = m.cfg.OpenLog(t.info)
	}
	requests, lastText := 0, ""
	result, err := t.conv.Run(limitCtx, t.prompt, func(event agent.Event) error {
		switch event.Type {
		case agent.EventRequestStart:
			requests++
		case agent.EventResponse:
			if event.Response != nil {
				if text := messageText(event.Response.Message); text != "" {
					lastText = text
				}
			}
		}
		if sink != nil {
			_ = sink.Record(event)
		}
		return nil
	})
	status := result.Status
	if err != nil && t.ctx.Err() == nil && errors.Is(limitCtx.Err(), context.DeadlineExceeded) {
		status = StatusTimeLimit
		err = fmt.Errorf("达到 %s 总时长上限", m.cfg.TimeLimit)
	}
	if t.ctx.Err() != nil {
		status = agent.StatusCanceled
	}
	m.finish(t, sink)

	ended := Event{Type: EventEnded, Info: t.info, Status: status, Partial: IsPartial(status), Requests: requests, Duration: time.Since(t.info.Started)}
	if err != nil {
		ended.Error = err.Error()
	}
	if status != agent.StatusCanceled {
		body := reportBody(status, err, lastText)
		_, ended.Truncated = truncateReport(body)
		ended.Report = FormatReport(t.info.ID, t.info.Description, status, body)
		ended.Injected = true
		m.parentConversation().Enqueue(agent.InboxItem{Source: "subagent:" + t.info.ID, Text: ended.Report})
	}
	m.emit(ended)
	m.dispatch()
}

// reportBody 组合子任务报告正文：正常结束就是最后一段文字，否则先写明原因再附已有输出。
func reportBody(status agent.Status, err error, lastText string) string {
	if status == agent.StatusCompleted {
		if lastText == "" {
			return "（子任务结束了，但没有产出文字报告。）"
		}
		return lastText
	}
	reason := "未知原因"
	if err != nil {
		reason = err.Error()
	}
	body := "子任务没有正常结束：" + reason
	if lastText != "" {
		body += "\n\n已有的输出：\n" + lastText
	}
	return body
}

func (m *Manager) parentConversation() *agent.Conversation {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.parent
}

// finish 释放子任务占用的资源：登记项、文件范围、工具输出存档和诊断记录。
func (m *Manager) finish(t *task, sink EventSink) {
	m.mu.Lock()
	delete(m.tasks, t.info.ID)
	m.mu.Unlock()
	m.claims.Release(t.info.ID)
	t.cancel()
	_ = t.conv.Close()
	if sink != nil {
		_ = sink.Close()
	}
}

// setWaiting 由审批网关调用：子任务开始或结束等待用户审批。
func (m *Manager) setWaiting(origin tool.Origin, waiting bool) {
	m.mu.Lock()
	t, ok := m.tasks[origin.ID]
	if !ok {
		m.mu.Unlock()
		return
	}
	if waiting {
		t.waiting++
	} else if t.waiting > 0 {
		t.waiting--
	}
	state := StateRunning
	if t.waiting > 0 {
		state = StateWaitingApproval
	}
	changed := t.info.State != state
	t.info.State = state
	info := t.info
	m.mu.Unlock()
	if changed {
		m.emit(Event{Type: EventState, Info: info})
	}
}

// Running 返回未结束的子任务数，包含等待审批和排队中的任务。
func (m *Manager) Running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tasks) + len(m.queue)
}

// Snapshot 返回未结束子任务的当前状态：运行中的按启动时间排序，排队中的按入队顺序排在后面。
func (m *Manager) Snapshot() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	infos := make([]Info, 0, len(m.tasks)+len(m.queue))
	for _, t := range m.tasks {
		infos = append(infos, t.info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Started.Before(infos[j].Started) })
	for _, t := range m.queue {
		infos = append(infos, t.info)
	}
	return infos
}

// CancelAll 取消所有运行中的子任务、丢弃排队中的任务，返回数量。不等待它们停止；被取消的子任务不注入结果。
// 排队中的任务从未启动，不产生启动或结束事件。
func (m *Manager) CancelAll() int {
	m.mu.Lock()
	queued := m.queue
	m.queue = nil
	for _, t := range m.tasks {
		t.cancel()
	}
	count := len(m.tasks) + len(queued)
	for _, t := range queued {
		m.discardLocked(t)
	}
	m.mu.Unlock()
	return count
}

// Wait 等待所有子任务的 goroutine 退出。
func (m *Manager) Wait() { m.wg.Wait() }

// Close 拒绝新的子任务，取消现有的并等待它们停止。
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.CancelAll()
	m.Wait()
}
