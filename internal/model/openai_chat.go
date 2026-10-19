package model

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

// openAIChatClient 通过官方 SDK 访问 OpenAI Chat Completions 协议。
type openAIChatClient struct{ options Options }

func (c *openAIChatClient) Stream(ctx context.Context, req Request, emit func(Event) error) (Response, error) {
	collector := newCollector(sourceFor(c.options, req.Model), emit)
	params, err := chatRequestParams(req)
	if err != nil {
		return collector.response, err
	}
	service := openai.NewChatService(openAIRequestOptions(c.options)...)
	stream := service.Completions.NewStreaming(ctx, params)
	defer func() { _ = stream.Close() }()
	state := &openAIChatStream{collector: collector, textBlock: -1, tools: make(map[int64]int)}
	for stream.Next() {
		if err := state.handle(stream.Current()); err != nil {
			return collector.response, err
		}
	}
	if ctx.Err() != nil {
		return collector.response, ctx.Err()
	}
	if err := stream.Err(); err != nil {
		return collector.response, classifyOpenAIStreamError(ctx, err)
	}
	if err := state.finish(); err != nil {
		return collector.response, err
	}
	return collector.response, nil
}

// openAIChatStream 把 Chat Completions 流式分片映射成通用内容块。
type openAIChatStream struct {
	collector    *collector
	textBlock    int           // 正文块索引，-1 表示尚未开始
	tools        map[int64]int // 服务端工具调用索引 → 内容块索引
	finishReason string
	refused      bool
}

func (s *openAIChatStream) handle(chunk openai.ChatCompletionChunk) error {
	if chunk.Usage.JSON.PromptTokens.Valid() || chunk.Usage.JSON.CompletionTokens.Valid() {
		s.collector.response.Usage = openAIChatUsage(chunk.Usage)
	}
	for _, choice := range chunk.Choices {
		if choice.Index != 0 {
			return protocolError("模型返回了多个候选回复")
		}
		if err := s.delta(choice.Delta); err != nil {
			return err
		}
		if choice.FinishReason != "" {
			if s.finishReason != "" && s.finishReason != choice.FinishReason {
				return protocolError("模型返回了相互矛盾的结束原因")
			}
			s.finishReason = choice.FinishReason
		}
	}
	return nil
}

func (s *openAIChatStream) delta(delta openai.ChatCompletionChunkChoiceDelta) error {
	if delta.FunctionCall.Name != "" || delta.FunctionCall.Arguments != "" {
		return &Error{Kind: ErrUnsupported, Message: "模型返回了已废弃的 function_call 增量，暂不支持"}
	}
	if len(delta.JSON.ExtraFields) > 0 {
		// 例如部分推理模型额外返回的 reasoning_content：无法无损表达时必须明确失败。
		return &Error{Kind: ErrUnsupported, Message: "模型返回了 Chat Completions 无法表达的增量内容，请改用 Responses 协议或更换模型"}
	}
	if delta.Refusal != "" {
		s.refused = true
	}
	if err := s.appendText(delta.Content + delta.Refusal); err != nil {
		return err
	}
	for _, call := range delta.ToolCalls {
		index, ok := s.tools[call.Index]
		if !ok {
			if call.ID == "" || call.Function.Name == "" {
				return protocolError("模型返回了不完整的工具调用")
			}
			block, err := s.collector.start(ContentBlock{Type: BlockToolCall, ToolCall: &ToolCall{ID: call.ID, Name: call.Function.Name}})
			if err != nil {
				return err
			}
			s.tools[call.Index] = block
			index = block
		} else if s.collector.response.Message.Content[index].Type != BlockToolCall {
			return protocolError("模型工具增量与内容块类型不一致")
		}
		if call.Function.Arguments != "" {
			if err := s.collector.delta(index, call.Function.Arguments); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *openAIChatStream) appendText(text string) error {
	if text == "" {
		return nil
	}
	if s.textBlock < 0 {
		index, err := s.collector.start(ContentBlock{Type: BlockText})
		if err != nil {
			return err
		}
		s.textBlock = index
	}
	return s.collector.delta(s.textBlock, text)
}

func (s *openAIChatStream) finish() error {
	if s.finishReason == "" {
		return protocolError("流式连接在模型返回结束原因前中断，请重试或检查中转站")
	}
	reason := StopEndTurn
	truncated := false
	switch s.finishReason {
	case "stop":
		reason = StopEndTurn
	case "tool_calls", "function_call":
		reason = StopToolUse
	case "length":
		reason, truncated = StopMaxTokens, true
	case "content_filter":
		reason = StopRefusal
	default:
		return protocolError("模型返回了无法识别的结束原因")
	}
	if reason == StopEndTurn && s.refused {
		reason = StopRefusal
	}
	if err := endOpenBlocks(s.collector, truncated); err != nil {
		return err
	}
	if truncated {
		// 输出被截断时未完成的工具调用保留在部分结果里，直接记录结束原因。
		s.collector.response.StopReason = reason
		return nil
	}
	return s.collector.complete(reason)
}

func chatRequestParams(req Request) (openai.ChatCompletionNewParams, error) {
	messages, err := chatMessages(req)
	if err != nil {
		return openai.ChatCompletionNewParams{}, err
	}
	tools, err := chatTools(req.Tools)
	if err != nil {
		return openai.ChatCompletionNewParams{}, err
	}
	return openai.ChatCompletionNewParams{
		Model:               shared.ChatModel(req.Model),
		Messages:            messages,
		MaxCompletionTokens: param.NewOpt(int64(req.MaxOutputTokens)),
		// 让服务在流末尾返回 token 用量。
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: param.NewOpt(true)},
		Tools:         tools,
	}, nil
}

// chatMessages 按消息顺序生成 Chat Completions 消息，并保持工具调用与工具结果的关联。
func chatMessages(req Request) ([]openai.ChatCompletionMessageParamUnion, error) {
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages)+1)
	if req.System != "" {
		messages = append(messages, openai.SystemMessage(req.System))
	}
	for _, message := range req.Messages {
		switch message.Role {
		case RoleUser:
			messages = append(messages, openai.UserMessage(messageText(message)))
		case RoleAssistant:
			var text []string
			assistant := openai.ChatCompletionAssistantMessageParam{}
			for _, block := range message.Content {
				switch block.Type {
				case BlockText:
					text = append(text, block.Text)
				case BlockToolCall:
					call := block.ToolCall
					assistant.ToolCalls = append(assistant.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
						ID: call.ID,
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name:      call.Name,
							Arguments: string(call.Arguments),
						},
					}})
				case BlockThinking:
					// Chat Completions 没有能无损表达思考续接的字段，不能静默丢弃。
					return nil, &Error{Kind: ErrUnsupported, Message: "Chat Completions 协议无法表达思考内容，请改用 Responses 协议续接"}
				}
			}
			if len(text) > 0 {
				assistant.Content.OfString = param.NewOpt(strings.Join(text, "\n"))
			}
			if assistant.Content.OfString.Valid() || len(assistant.ToolCalls) > 0 {
				messages = append(messages, openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant})
			}
		case RoleTool:
			for _, block := range message.Content {
				result := block.ToolResult
				messages = append(messages, openai.ToolMessage(toolResultText(result), result.ToolCallID))
			}
		}
	}
	return messages, nil
}

func chatTools(tools []ToolDefinition) ([]openai.ChatCompletionToolUnionParam, error) {
	result := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		var schema shared.FunctionParameters
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil || schema == nil {
			return nil, invalidRequest("工具输入 schema 必须是 JSON 对象")
		}
		result = append(result, openai.ChatCompletionToolUnionParam{OfFunction: &openai.ChatCompletionFunctionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        tool.Name,
				Description: param.NewOpt(tool.Description),
				Parameters:  schema,
			},
		}})
	}
	return result, nil
}

func openAIChatUsage(usage openai.CompletionUsage) Usage {
	result := Usage{}
	if usage.JSON.PromptTokens.Valid() {
		result.InputTokens = int64ptr(usage.PromptTokens)
	}
	if usage.JSON.CompletionTokens.Valid() {
		result.OutputTokens = int64ptr(usage.CompletionTokens)
	}
	if usage.JSON.PromptTokensDetails.Valid() {
		if usage.PromptTokensDetails.JSON.CachedTokens.Valid() {
			result.CacheReadTokens = int64ptr(usage.PromptTokensDetails.CachedTokens)
		}
		if usage.PromptTokensDetails.JSON.CacheWriteTokens.Valid() {
			result.CacheWriteTokens = int64ptr(usage.PromptTokensDetails.CacheWriteTokens)
		}
	}
	if usage.JSON.CompletionTokensDetails.Valid() && usage.CompletionTokensDetails.JSON.ReasoningTokens.Valid() {
		result.ReasoningTokens = int64ptr(usage.CompletionTokensDetails.ReasoningTokens)
	}
	return result
}
