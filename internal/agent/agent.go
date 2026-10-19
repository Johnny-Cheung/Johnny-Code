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

// Run 处理一个用户任务；不会读取或恢复旧会话。
func Run(ctx context.Context, opts Options, prompt string, emit func(Event) error) (result Result, err error) {
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
	notify := func(event Event) error {
		if emit == nil {
			return nil
		}
		if emitErr := emit(event); emitErr != nil {
			return fmt.Errorf("处理任务事件失败：%w", emitErr)
		}
		return nil
	}
	defer func() {
		result.Status = statusFor(err)
		endErr := notify(Event{Type: EventEnd, Turn: result.Turns, Status: result.Status, Error: errorText(err)})
		if err == nil && endErr != nil {
			err = endErr
			result.Status = StatusFailed
		}
	}()
	user := model.Message{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.BlockText, Text: prompt}}}
	messages := []model.Message{user}
	if err = notify(Event{Type: EventStart, Message: &user}); err != nil {
		return result, err
	}
	seen := make(map[string]bool)
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
			Messages: append([]model.Message(nil), messages...),
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
		messages = append(messages, response.Message)
		switch response.StopReason {
		case model.StopMaxTokens:
			return result, ErrMaxTokens
		case model.StopRefusal:
			return result, ErrRefusal
		case model.StopEndTurn, model.StopToolUse:
		default:
			return result, protocolError("模型返回了未知结束原因")
		}
		calls, callErr := responseCalls(response.Message, seen)
		if callErr != nil {
			return result, callErr
		}
		if response.StopReason == model.StopEndTurn {
			if len(calls) != 0 {
				return result, protocolError("模型正常结束状态仍包含工具调用")
			}
			return result, nil
		}
		if len(calls) == 0 {
			return result, protocolError("模型请求工具但没有返回工具调用")
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
			messages = append(messages, model.Message{Role: model.RoleTool, Content: []model.ContentBlock{{Type: model.BlockToolResult, ToolResult: &converted}}})
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

func responseCalls(message model.Message, seen map[string]bool) ([]model.ToolCall, error) {
	if message.Role != model.RoleAssistant {
		return nil, protocolError("模型响应必须是 assistant 消息")
	}
	var calls []model.ToolCall
	for _, block := range message.Content {
		if block.Type != model.BlockToolCall {
			continue
		}
		call := block.ToolCall
		if call == nil || strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" || seen[call.ID] || !json.Valid(call.Arguments) || len(strings.TrimSpace(string(call.Arguments))) == 0 || strings.TrimSpace(string(call.Arguments))[0] != '{' {
			return nil, protocolError("工具调用必须包含唯一 ID、名称和完整 JSON 对象参数")
		}
		seen[call.ID] = true
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
