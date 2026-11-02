package contextview

import (
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

// RestoreSummary 是恢复时使用的摘要。
type RestoreSummary struct {
	Text           string
	CoveredThrough int
	Note           bool
}

// RestoreStep 是按顺序重放的一条消息。工具结果额外带上原始调用，
// 以便按恢复时的预览预算重新生成视图。
type RestoreStep struct {
	UserTurn int
	Message  model.Message
	Call     *model.ToolCall
}

// Restore 用摘要和保留轮次初始化一个空 Manager。
//
// 它恢复的是原进程最后一次请求视图：摘要作为第一条合成 user 消息，
// 之后只放摘要覆盖轮次之后的消息；更早的历史不进入请求视图。
// 每个轮次结束时会重新执行轮级压缩，因此恢复的是等价视图，不保证逐字节相同。
func (m *Manager) Restore(summary *RestoreSummary, steps []RestoreStep) {
	if m == nil {
		return
	}
	m.request = nil
	m.turns = nil
	m.current = -1
	m.inTurn = false
	m.callSeq = 0
	m.summary = ""
	m.summaryTurns = 0
	m.summaryCovered = 0
	m.summaryIsNote = false

	if summary != nil && strings.TrimSpace(summary.Text) != "" {
		m.summary = summary.Text
		m.summaryCovered = summary.CoveredThrough
		m.summaryTurns = summary.CoveredThrough
		m.summaryIsNote = summary.Note
		m.request = append(m.request, model.Message{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.BlockText, Text: summary.Text}},
		})
	}

	currentTurn := 0
	for _, step := range steps {
		switch step.Message.Role {
		case model.RoleUser:
			if currentTurn == 0 || currentTurn != step.UserTurn {
				if m.inTurn {
					m.FinishTurn()
				}
				m.BeginTurn(step.UserTurn, step.Message)
				currentTurn = step.UserTurn
			} else {
				// 同一轮内的合成 user 消息（收件箱注入）追加到当前轮。
				m.AppendUser(step.Message)
			}
		case model.RoleAssistant:
			if !m.inTurn {
				continue
			}
			m.AppendAssistant(step.Message)
		case model.RoleTool:
			if !m.inTurn || step.Call == nil {
				continue
			}
			result := toolResult(step.Message)
			if result == nil {
				continue
			}
			m.AppendToolResult(step.UserTurn, *step.Call, *result)
		}
	}
	if m.inTurn {
		m.FinishTurn()
	}
}

func toolResult(message model.Message) *model.ToolResult {
	for _, block := range message.Content {
		if block.Type == model.BlockToolResult && block.ToolResult != nil {
			return block.ToolResult
		}
	}
	return nil
}
