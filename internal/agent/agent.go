package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

// Conversation 保存当前进程内的消息历史，按顺序处理用户输入，不读取旧日志。
// 同一个 Conversation 不支持并发调用 Run。
type Conversation struct {
	options  Options
	messages []model.Message
	seen     map[string]bool
	userTurn int
}

func NewConversation(opts Options) *Conversation {
	return &Conversation{options: opts, seen: make(map[string]bool)}
}

// Run 处理一个独立用户任务；每次调用都从新会话开始。
func Run(ctx context.Context, opts Options, prompt string, emit func(Event) error) (Result, error) {
	return NewConversation(opts).Run(ctx, prompt, emit)
}

// Run 在已有历史上处理下一轮输入，每轮独立计算模型请求次数。
func (c *Conversation) Run(ctx context.Context, prompt string, emit func(Event) error) (result Result, err error) {
	result.Status = StatusFailed
	if c == nil {
		return result, fmt.Errorf("会话不能为空")
	}
	opts := c.options
	if opts.MaxTurns == 0 {
		opts.MaxTurns = DefaultMaxTurns
	}
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = DefaultRequestTimeout
	}
	if opts.MaxOutputTokens == 0 {
		opts.MaxOutputTokens = 4096
	}
	if opts.Client == nil || opts.Tools == nil || strings.TrimSpace(opts.Model) == "" || strings.TrimSpace(prompt) == "" || opts.MaxTurns < 1 || opts.RequestTimeout < 0 || opts.MaxOutputTokens < 1 {
		return result, fmt.Errorf("模型、工具、模型名、任务及正数执行上限必须提供")
	}
	c.userTurn++
	result.UserTurn = c.userTurn
	notify := func(event Event) error {
		event.UserTurn = c.userTurn
		if emit == nil {
			return nil
		}
		if emitErr := emit(event); emitErr != nil {
			return fmt.Errorf("处理任务事件失败：%w", emitErr)
		}
		return nil
	}
	var pending []model.ToolCall
	defer func() {
		// 保留已执行结果，未执行调用补显式错误结果，避免下一轮带着悬空 ID。
		for _, call := range pending {
			skipped := model.ToolResult{ToolCallID: call.ID, Content: "工具未执行：本轮已中止。" + errorText(err), IsError: true}
			c.appendToolResult(skipped)
			_ = notify(Event{Type: EventToolSkipped, Turn: result.Turns, Call: &call, ToolResult: &skipped, Error: errorText(err)})
		}
		result.Status = statusFor(err)
		endErr := notify(Event{Type: EventEnd, Turn: result.Turns, Status: result.Status, Error: errorText(err)})
		if err == nil && endErr != nil {
			err = endErr
			result.Status = StatusFailed
		}
	}()
	user := model.Message{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.BlockText, Text: prompt}}}
	c.messages = append(c.messages, user)
	if err = notify(Event{Type: EventStart, Message: &user}); err != nil {
		return result, err
	}
	for turn := 1; turn <= opts.MaxTurns; turn++ {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if err = notify(Event{Type: EventRequestStart, Turn: turn}); err != nil {
			return result, err
		}
		if err = ctx.Err(); err != nil {
			return result, err
		}
		result.Turns = turn
		requestCtx, cancel := context.WithTimeout(ctx, opts.RequestTimeout)
		response, streamErr := opts.Client.Stream(requestCtx, model.Request{
			Model: opts.Model, System: opts.System,
			Messages: append([]model.Message(nil), c.messages...),
			Tools:    opts.Tools.Definitions(), MaxOutputTokens: opts.MaxOutputTokens,
		}, func(event model.Event) error {
			if ctxErr := requestCtx.Err(); ctxErr != nil {
				return ctxErr
			}
			return notify(Event{Type: EventModel, Turn: turn, ModelEvent: &event})
		})
		if streamErr == nil {
			streamErr = requestCtx.Err()
		}
		cancel()
		result.Message = response.Message
		if err = notify(Event{Type: EventResponse, Turn: turn, Response: &response, Error: errorText(streamErr)}); err != nil {
			return result, err
		}
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if streamErr != nil {
			return result, fmt.Errorf("第 %d 次模型请求失败：%w", turn, streamErr)
		}
		switch response.StopReason {
		case model.StopMaxTokens:
			return result, ErrMaxTokens
		case model.StopRefusal:
			return result, ErrRefusal
		case model.StopEndTurn, model.StopToolUse:
		default:
			return result, protocolError("模型返回了未知结束原因")
		}
		calls, callErr := responseCalls(response.Message, c.seen)
		if callErr != nil {
			return result, callErr
		}
		if response.StopReason == model.StopEndTurn && len(calls) != 0 {
			return result, protocolError("模型正常结束状态仍包含工具调用")
		}
		if response.StopReason == model.StopToolUse && len(calls) == 0 {
			return result, protocolError("模型请求工具但没有返回工具调用")
		}
		// 只有可续接的完整响应进入会话历史；失败或截断原文仍交给诊断记录。
		c.messages = append(c.messages, response.Message)
		for _, call := range calls {
			c.seen[call.ID] = true
		}
		pending = calls
		if response.StopReason == model.StopEndTurn {
			return result, nil
		}
		if turn == opts.MaxTurns {
			return result, ErrMaxTurns
		}
		for _, call := range calls {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			if err = notify(Event{Type: EventToolStart, Turn: turn, Call: &call}); err != nil {
				return result, err
			}
			if err = ctx.Err(); err != nil {
				return result, err
			}
			// 模型请求的子 context 已释放；工具使用任务 context 和自身超时。
			value, execErr := opts.Tools.Execute(ctx, call)
			converted := tool.ToModelResult(call.ID, value, execErr)
			c.appendToolResult(converted)
			pending = pending[1:]
			if err = notify(Event{Type: EventToolEnd, Turn: turn, Call: &call, ToolResult: &converted, Error: errorText(execErr)}); err != nil {
				return result, err
			}
			// 工具自身 DeadlineExceeded 是普通工具错误；只检查父任务是否已结束。
			if err = ctx.Err(); err != nil {
				return result, err
			}
		}
	}
	return result, ErrMaxTurns
}

func (c *Conversation) appendToolResult(result model.ToolResult) {
	c.messages = append(c.messages, model.Message{Role: model.RoleTool, Content: []model.ContentBlock{{Type: model.BlockToolResult, ToolResult: &result}}})
}

func responseCalls(message model.Message, seen map[string]bool) ([]model.ToolCall, error) {
	if message.Role != model.RoleAssistant {
		return nil, protocolError("模型响应必须是 assistant 消息")
	}
	var calls []model.ToolCall
	batch := make(map[string]bool)
	for _, block := range message.Content {
		if block.Type != model.BlockToolCall {
			continue
		}
		call := block.ToolCall
		if call == nil || strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" || seen[call.ID] || batch[call.ID] || !json.Valid(call.Arguments) || len(strings.TrimSpace(string(call.Arguments))) == 0 || strings.TrimSpace(string(call.Arguments))[0] != '{' {
			return nil, protocolError("工具调用必须包含唯一 ID、名称和完整 JSON 对象参数")
		}
		batch[call.ID] = true
		calls = append(calls, *call)
	}
	return calls, nil
}

func protocolError(message string) error {
	return &model.Error{Kind: model.ErrProtocol, Message: message}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func statusFor(err error) Status {
	if err == nil {
		return StatusCompleted
	}
	switch {
	case errors.Is(err, context.Canceled):
		return StatusCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return StatusRequestTimeout
	case errors.Is(err, ErrMaxTurns):
		return StatusMaxTurns
	case errors.Is(err, ErrMaxTokens):
		return StatusMaxTokens
	case errors.Is(err, ErrRefusal):
		return StatusRefusal
	default:
		return StatusFailed
	}
}
