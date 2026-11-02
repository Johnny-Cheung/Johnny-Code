package agent

import (
	"errors"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

// RestoreState 是从本地会话记录恢复出来的请求视图状态。
type RestoreState struct {
	// Summary 是最近一次完整历史压缩的结果；nil 表示没有压缩过。
	Summary *contextview.RestoreSummary
	// Steps 已经按摘要裁剪，只包含摘要覆盖轮次之后的消息。
	Steps []contextview.RestoreStep
	// UserTurn 是最后一个完整轮次的编号，下一轮从它加一开始。
	UserTurn int
}

// RestoreConversation 构造一个从历史恢复的会话。
//
// 它只恢复请求视图需要的消息，不恢复旧权限授权、子任务或工作流状态。
// 系统提示和长期规则由调用方用当前文件重新构建后通过 Options 传入。
func RestoreConversation(opts Options, state RestoreState) (*Conversation, error) {
	if opts.Client == nil || opts.Tools == nil || strings.TrimSpace(opts.Model) == "" {
		return nil, errors.New("模型客户端、工具和模型名必须提供")
	}
	conversation := NewConversation(opts)
	if state.UserTurn < 0 {
		state.UserTurn = 0
	}
	conversation.userTurn = state.UserTurn
	conversation.view.Restore(state.Summary, state.Steps)
	conversation.messages = make([]model.Message, 0, len(state.Steps))
	conversation.seen = make(map[string]bool)
	for _, step := range state.Steps {
		conversation.messages = append(conversation.messages, step.Message)
		for _, block := range step.Message.Content {
			if block.ToolCall != nil {
				conversation.seen[block.ToolCall.ID] = true
			}
		}
	}
	return conversation, nil
}
