package contextview

import "github.com/Johnny-Cheung/Johnny-Code/internal/model"

// AppendUser 在当前轮末尾追加一条合成 user 消息（例如子任务结果），不开启新轮次。
func (m *Manager) AppendUser(message model.Message) {
	if m == nil {
		return
	}
	m.request = append(m.request, cloneMessage(message))
	if m.current >= 0 && m.current < len(m.turns) {
		m.turns[m.current].End = len(m.request)
	}
}

// Fork 返回请求视图的独立副本，供 fork 子会话作为起点。
//
// 副本只含最后一个完整消息边界之前的内容：还有未回填结果的 tool_call 及其后的消息
// 被截掉，避免子会话的第一次请求带着悬空调用。摘要状态和校准系数一并带走，
// 熔断计数、用量指标和轮内状态不带走。副本使用 sessionID 创建新的工具输出存档
// 命名空间，调用方负责在子会话结束时 Close。
func (m *Manager) Fork(sessionID string) *Manager {
	if m == nil {
		return nil
	}
	workDir := ""
	if m.artifacts != nil {
		workDir = m.artifacts.workDir
	}
	child := NewManager(Config{Budget: m.budget, WorkDir: workDir, SessionID: sessionID, PreviewBytes: m.previewBytes})
	cut := completePrefix(m.request)
	child.request = cloneMessages(m.request[:cut])
	for _, turn := range m.turns {
		if turn.Start >= cut {
			break
		}
		copied := turn
		copied.End = min(turn.End, cut)
		copied.Results = nil
		for _, ref := range turn.Results {
			if ref.Message < cut {
				copied.Results = append(copied.Results, ref)
			}
		}
		child.turns = append(child.turns, copied)
	}
	child.current = len(child.turns) - 1
	child.summary = m.summary
	child.summaryTurns = m.summaryTurns
	child.summaryCovered = m.summaryCovered
	child.summaryIsNote = m.summaryIsNote
	child.calibration = m.calibration
	return child
}

// completePrefix 返回最长的、所有 tool_call 都已有结果的消息前缀长度。
func completePrefix(messages []model.Message) int {
	open := make(map[string]bool)
	clean := 0
	for i, message := range messages {
		for _, block := range message.Content {
			if block.ToolCall != nil {
				open[block.ToolCall.ID] = true
			}
			if block.ToolResult != nil {
				delete(open, block.ToolResult.ToolCallID)
			}
		}
		if len(open) == 0 {
			clean = i + 1
		}
	}
	return clean
}
