package agent

import (
	"errors"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

// ForkMaxUsablePercent 是 fork 允许的父请求视图占可用输入预算的上限。
// 超过时拒绝 fork，因为此时父会话正处于一轮中间，不能为了 fork 去压缩它。
const ForkMaxUsablePercent = 50

var ErrForkTooLarge = errors.New("主对话上下文已超过可用输入预算的 50%，不适合 fork，请改用 fresh")

// ForkOptions 描述 fork 子会话的运行参数。模型客户端、模型名、系统提示和
// 输出上限沿用父会话。
type ForkOptions struct {
	// Tools 是子会话的工具集合。Definitions 应与父会话一致，使子会话沿用同一份请求前缀。
	Tools Tools
	// SessionID 命名子会话的工具输出存档目录。
	SessionID      string
	MaxTurns       int
	RequestTimeout time.Duration
}

// Fork 以父会话请求视图的快照创建子会话：截到最后一个完整消息边界，
// 带着摘要，之后的 Run 把子任务指令作为新的用户轮追加。
// 子会话不加载记忆、不做轮末记忆审查，也不共享收件箱。
//
// Fork 只能在父会话空闲时或父会话 Run 的工具执行期间（同一 goroutine）调用。
func (c *Conversation) Fork(opts ForkOptions) (*Conversation, error) {
	if c == nil || c.view == nil {
		return nil, errors.New("会话不能为空")
	}
	if opts.Tools == nil {
		return nil, errors.New("fork 需要子会话的工具集合")
	}
	var definitions []model.ToolDefinition
	if c.options.Tools != nil {
		definitions = c.options.Tools.Definitions()
	}
	view := c.view.Fork(opts.SessionID)
	if view.Estimate(c.system(), definitions)*100 > view.Budget().Usable*ForkMaxUsablePercent {
		_ = view.Close()
		return nil, ErrForkTooLarge
	}
	options := c.options
	options.Tools = opts.Tools
	options.MaxTurns = opts.MaxTurns
	options.RequestTimeout = opts.RequestTimeout
	options.ContextView = view
	options.Pinned = nil
	options.MemoryReview = nil
	child := &Conversation{
		options:    options,
		seen:       make(map[string]bool),
		userTurn:   c.userTurn,
		view:       view,
		pinnedText: c.pinnedText,
		messages:   view.Messages(),
	}
	for _, message := range child.messages {
		for _, block := range message.Content {
			if block.ToolCall != nil {
				child.seen[block.ToolCall.ID] = true
			}
		}
	}
	return child, nil
}

// Close 删除本会话的工具输出存档。主会话在退出时调用，子会话在结束时调用。
func (c *Conversation) Close() error {
	if c == nil {
		return nil
	}
	return c.view.Close()
}
