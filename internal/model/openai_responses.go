package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// 思考项的文本来源：同一个思考项只采用先出现的一类，避免摘要与原始推理重复拼接。
const (
	reasoningSummaryText = "summary"
	reasoningRawText     = "raw"
)

// openAIResponsesClient 通过官方 SDK 访问 OpenAI Responses 协议。
type openAIResponsesClient struct{ options Options }

func (c *openAIResponsesClient) Stream(ctx context.Context, req Request, emit func(Event) error) (Response, error) {
	collector := newCollector(sourceFor(c.options, req.Model), emit)
	params, err := responsesRequestParams(req)
	if err != nil {
		return collector.response, err
	}
	service := responses.NewResponseService(openAIRequestOptions(c.options)...)
	stream := service.NewStreaming(ctx, params)
	defer func() { _ = stream.Close() }()
	state := &openAIResponsesStream{
		collector:     collector,
		source:        *collector.response.Message.Source,
		blocks:        make(map[int64]int),
		reasoningKind: make(map[int64]string),
	}
	for stream.Next() {
		if err := state.handle(stream.Current()); err != nil {
			return collector.response, err
		}
		if state.terminal {
			break
		}
	}
	if ctx.Err() != nil {
		return collector.response, ctx.Err()
	}
	if err := stream.Err(); err != nil {
		return collector.response, classifyOpenAIStreamError(ctx, err)
	}
	if !state.terminal {
		return collector.response, protocolError("流式连接在模型完成回复前结束，请重试或检查中转站")
	}
	return collector.response, nil
}

// openAIResponsesStream 把 Responses 流式事件按输出项索引映射成通用内容块。
type openAIResponsesStream struct {
	collector     *collector
	source        Source
	blocks        map[int64]int    // 服务端输出项索引 → 内容块索引
	reasoningKind map[int64]string // 思考项的文本来源，用于去重
	refused       bool
	terminal      bool
}

func (s *openAIResponsesStream) handle(event responses.ResponseStreamEventUnion) error {
	switch event.Type {
	case "response.output_item.added":
		return s.itemAdded(event.OutputIndex, event.Item)
	case "response.output_item.done":
		return s.itemDone(event.OutputIndex, event.Item)
	case "response.output_text.delta":
		return s.textDelta(event.OutputIndex, event.Delta)
	case "response.refusal.delta":
		s.refused = true
		return s.textDelta(event.OutputIndex, event.Delta)
	case "response.reasoning_summary_text.delta":
		return s.reasoningDelta(event.OutputIndex, reasoningSummaryText, event.Delta)
	case "response.reasoning_text.delta":
		return s.reasoningDelta(event.OutputIndex, reasoningRawText, event.Delta)
	case "response.function_call_arguments.delta":
		index, ok := s.blocks[event.OutputIndex]
		if !ok {
			return protocolError("模型在工具调用开始前发送了参数增量")
		}
		if s.collector.response.Message.Content[index].Type != BlockToolCall {
			return protocolError("模型工具参数增量与输出项类型不一致")
		}
		return s.collector.delta(index, event.Delta)
	case "response.completed":
		return s.finish(event.Response, false)
	case "response.incomplete":
		return s.finish(event.Response, true)
	case "response.failed":
		return &Error{Kind: ErrService, Message: "模型服务返回了失败状态，请检查模型名、参数或服务状态"}
	case "error":
		return &Error{Kind: ErrService, Message: "模型服务在流中返回错误，请检查 Key、额度、模型名或中转站状态"}
	default:
		// 创建、进行中、内容分段与结束通知等事件不携带新内容，忽略即可。
		return nil
	}
}

func (s *openAIResponsesStream) itemAdded(outputIndex int64, item responses.ResponseOutputItemUnion) error {
	if _, exists := s.blocks[outputIndex]; exists {
		return protocolError("模型重复开始同一个输出项")
	}
	switch item.Type {
	case "message":
		_, err := s.startBlock(outputIndex, ContentBlock{Type: BlockText})
		return err
	case "reasoning":
		_, err := s.startBlock(outputIndex, ContentBlock{Type: BlockThinking})
		return err
	case "function_call":
		if item.CallID == "" || item.Name == "" {
			return protocolError("模型返回了不完整的工具调用")
		}
		_, err := s.startBlock(outputIndex, ContentBlock{Type: BlockToolCall, ToolCall: &ToolCall{ID: item.CallID, Name: item.Name}})
		return err
	default:
		return &Error{Kind: ErrUnsupported, Message: "模型返回了尚不支持的 Responses 输出项"}
	}
}

func (s *openAIResponsesStream) itemDone(outputIndex int64, item responses.ResponseOutputItemUnion) error {
	index, ok := s.blocks[outputIndex]
	if !ok {
		return protocolError("模型结束了尚未开始的输出项")
	}
	if err := s.applyItem(index, item); err != nil {
		return err
	}
	block := &s.collector.response.Message.Content[index]
	if block.Type == BlockToolCall && !toolCallComplete(block) {
		// 输出被截断时工具参数可能不完整，留到终态判断能否作为部分结果保留。
		return nil
	}
	return s.collector.end(index)
}

func (s *openAIResponsesStream) textDelta(outputIndex int64, delta string) error {
	if delta == "" {
		return nil
	}
	index, ok := s.blocks[outputIndex]
	if !ok {
		var err error
		if index, err = s.startBlock(outputIndex, ContentBlock{Type: BlockText}); err != nil {
			return err
		}
	}
	if s.collector.response.Message.Content[index].Type != BlockText {
		return protocolError("模型文本增量与输出项类型不一致")
	}
	return s.collector.delta(index, delta)
}

func (s *openAIResponsesStream) reasoningDelta(outputIndex int64, kind, delta string) error {
	if delta == "" {
		return nil
	}
	if seen, ok := s.reasoningKind[outputIndex]; ok && seen != kind {
		return nil
	}
	s.reasoningKind[outputIndex] = kind
	index, ok := s.blocks[outputIndex]
	if !ok {
		var err error
		if index, err = s.startBlock(outputIndex, ContentBlock{Type: BlockThinking}); err != nil {
			return err
		}
	}
	if s.collector.response.Message.Content[index].Type != BlockThinking {
		return protocolError("模型思考增量与输出项类型不一致")
	}
	return s.collector.delta(index, delta)
}

func (s *openAIResponsesStream) startBlock(outputIndex int64, block ContentBlock) (int, error) {
	index, err := s.collector.start(block)
	if err != nil {
		return index, err
	}
	s.blocks[outputIndex] = index
	return index, nil
}

// applyItem 把输出项的最终内容补进对应块，只在流式增量缺失时补齐，正文与工具参数都不会重复。
func (s *openAIResponsesStream) applyItem(index int, item responses.ResponseOutputItemUnion) error {
	block := &s.collector.response.Message.Content[index]
	switch block.Type {
	case BlockText:
		block.Replay = &Replay{Source: s.source, Data: json.RawMessage(item.RawJSON())}
		for _, part := range item.Content {
			s.refused = s.refused || part.Type == "refusal"
		}
		text := responsesItemText(item)
		if text != "" && !strings.HasPrefix(text, block.Text) {
			return protocolError("模型最终正文与流式增量不一致")
		}
		if len(text) > len(block.Text) {
			return s.collector.delta(index, text[len(block.Text):])
		}
	case BlockThinking:
		// 保留思考项的原始数据（含加密推理内容），供同一服务与模型无状态重放。
		data := item.RawJSON()
		if !json.Valid([]byte(data)) {
			return protocolError("模型返回的思考数据无法解析")
		}
		block.Replay = &Replay{Source: s.source, Data: json.RawMessage(data)}
	case BlockToolCall:
		// 保留工具调用输出项的原始数据（含输出项 ID），供同一服务与模型续接。
		data := item.RawJSON()
		if !json.Valid([]byte(data)) {
			return protocolError("模型返回的工具调用数据无法解析")
		}
		block.Replay = &Replay{Source: s.source, Data: json.RawMessage(data)}
		arguments := item.Arguments.OfString
		if len(block.ToolCall.Arguments) == 0 && arguments != "" && arguments != "{}" {
			block.ToolCall.Arguments = json.RawMessage(arguments)
		}
	}
	return nil
}

// applyResponseOutput 用终态响应里的输出项补齐尚未结束的块，兼容不发送 output_item.done 的中转服务。
func (s *openAIResponsesStream) applyResponseOutput(items []responses.ResponseOutputItemUnion) error {
	for position, item := range items {
		index, ok := s.blocks[int64(position)]
		if !ok {
			if err := s.itemAdded(int64(position), item); err != nil {
				return err
			}
			index = s.blocks[int64(position)]
		}
		if s.collector.ended[index] {
			block := &s.collector.response.Message.Content[index]
			if block.Replay != nil && json.Valid([]byte(item.RawJSON())) {
				block.Replay.Data = json.RawMessage(item.RawJSON())
			}
			continue
		}
		if err := s.applyItem(index, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *openAIResponsesStream) finish(response responses.Response, incomplete bool) error {
	s.terminal = true
	if err := s.applyResponseOutput(response.Output); err != nil {
		return err
	}
	s.collector.response.Usage = openAIResponsesUsage(response.Usage)
	toolCall := false
	for _, block := range s.collector.response.Message.Content {
		toolCall = toolCall || block.Type == BlockToolCall
	}
	reason := StopEndTurn
	if incomplete {
		switch response.IncompleteDetails.Reason {
		case "max_output_tokens":
			reason = StopMaxTokens
		case "content_filter":
			reason = StopRefusal
		default:
			return protocolError("模型未返回可识别的结束原因")
		}
	} else if toolCall {
		reason = StopToolUse
	} else if s.refused {
		reason = StopRefusal
	}
	if err := endOpenBlocks(s.collector, incomplete); err != nil {
		return err
	}
	if incomplete {
		// 截断时未完成的工具调用保留在部分结果里，直接记录结束原因。
		s.collector.response.StopReason = reason
		return nil
	}
	return s.collector.complete(reason)
}

// responsesItemText 提取消息输出项里的正文与拒绝文本。
func responsesItemText(item responses.ResponseOutputItemUnion) string {
	var text strings.Builder
	for _, part := range item.Content {
		switch part.Type {
		case "output_text":
			text.WriteString(part.Text)
		case "refusal":
			text.WriteString(part.Refusal)
		}
	}
	return text.String()
}

func responsesRequestParams(req Request) (responses.ResponseNewParams, error) {
	input, err := responsesInput(req.Messages)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	tools, err := responsesTools(req.Tools)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(req.Model),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: input},
		// 服务端不落库：会话状态完全由本进程持有，同一服务与模型可无状态重放。
		Store:           param.NewOpt(false),
		MaxOutputTokens: param.NewOpt(int64(req.MaxOutputTokens)),
		// 无状态续接思考内容必须随响应返回加密推理数据。
		Include: []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
		Tools:   tools,
	}
	if req.System != "" {
		params.Instructions = param.NewOpt(req.System)
	}
	return params, nil
}

// responsesInput 按消息顺序生成 Responses 输入项，保留思考与工具调用的续接数据。
func responsesInput(messages []Message) (responses.ResponseInputParam, error) {
	input := make(responses.ResponseInputParam, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case RoleUser:
			input = append(input, responses.ResponseInputItemUnionParam{OfMessage: responsesTextMessage(responses.EasyInputMessageRoleUser, messageText(message))})
		case RoleAssistant:
			var text []string
			flush := func() {
				if len(text) == 0 {
					return
				}
				input = append(input, responses.ResponseInputItemUnionParam{OfMessage: responsesTextMessage(responses.EasyInputMessageRoleAssistant, strings.Join(text, "\n"))})
				text = text[:0]
			}
			for _, block := range message.Content {
				switch block.Type {
				case BlockText:
					if block.Replay != nil {
						if !jsonObject(block.Replay.Data) {
							return nil, invalidRequest("文本消息续接数据无效")
						}
						flush()
						item := param.Override[responses.ResponseOutputMessageParam](json.RawMessage(block.Replay.Data))
						input = append(input, responses.ResponseInputItemUnionParam{OfOutputMessage: &item})
					} else {
						text = append(text, block.Text)
					}
				case BlockThinking:
					flush()
					item, err := responsesReasoningItem(block)
					if err != nil {
						return nil, err
					}
					input = append(input, item)
				case BlockToolCall:
					flush()
					input = append(input, responsesToolCallItem(block))
				}
			}
			flush()
		case RoleTool:
			for _, block := range message.Content {
				result := block.ToolResult
				input = append(input, responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					CallID: param.NewOpt(result.ToolCallID),
					Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: param.NewOpt(toolResultText(result))},
				}})
			}
		}
	}
	return input, nil
}

func responsesTextMessage(role responses.EasyInputMessageRole, text string) *responses.EasyInputMessageParam {
	return &responses.EasyInputMessageParam{Role: role, Content: responses.EasyInputMessageContentUnionParam{OfString: param.NewOpt(text)}}
}

// responsesToolCallItem 重放工具调用：优先使用保存的原始输出项（含输出项 ID 等字段），
// 缺失时用通用字段重建。
func responsesToolCallItem(block ContentBlock) responses.ResponseInputItemUnionParam {
	if block.Replay != nil && len(block.Replay.Data) > 0 && json.Valid(block.Replay.Data) {
		item := param.Override[responses.ResponseFunctionToolCallParam](json.RawMessage(block.Replay.Data))
		return responses.ResponseInputItemUnionParam{OfFunctionCall: &item}
	}
	call := block.ToolCall
	item := responses.ResponseFunctionToolCallParam{CallID: call.ID, Name: call.Name, Arguments: string(call.Arguments)}
	return responses.ResponseInputItemUnionParam{OfFunctionCall: &item}
}

// responsesReasoningItem 用保存的专用数据重放思考项，跨服务、协议或模型的数据已在校验层拒绝。
func responsesReasoningItem(block ContentBlock) (responses.ResponseInputItemUnionParam, error) {
	if block.Replay == nil || len(block.Replay.Data) == 0 {
		return responses.ResponseInputItemUnionParam{}, &Error{Kind: ErrUnsupported, Message: "思考块缺少可重放的专用数据"}
	}
	if !json.Valid(block.Replay.Data) {
		return responses.ResponseInputItemUnionParam{}, &Error{Kind: ErrUnsupported, Message: "思考块的专用重放数据无法解析"}
	}
	item := param.Override[responses.ResponseReasoningItemParam](json.RawMessage(block.Replay.Data))
	return responses.ResponseInputItemUnionParam{OfReasoning: &item}, nil
}

func responsesTools(tools []ToolDefinition) ([]responses.ToolUnionParam, error) {
	result := make([]responses.ToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil || schema == nil {
			return nil, invalidRequest("工具输入 schema 必须是 JSON 对象")
		}
		result = append(result, responses.ToolUnionParam{OfFunction: &responses.FunctionToolParam{
			Name:        tool.Name,
			Description: param.NewOpt(tool.Description),
			Parameters:  schema,
		}})
	}
	return result, nil
}

func openAIResponsesUsage(usage responses.ResponseUsage) Usage {
	result := Usage{}
	if usage.JSON.InputTokens.Valid() {
		result.InputTokens = int64ptr(usage.InputTokens)
	}
	if usage.JSON.OutputTokens.Valid() {
		result.OutputTokens = int64ptr(usage.OutputTokens)
	}
	if usage.JSON.InputTokensDetails.Valid() {
		if usage.InputTokensDetails.JSON.CachedTokens.Valid() {
			result.CacheReadTokens = int64ptr(usage.InputTokensDetails.CachedTokens)
		}
		if usage.InputTokensDetails.JSON.CacheWriteTokens.Valid() {
			result.CacheWriteTokens = int64ptr(usage.InputTokensDetails.CacheWriteTokens)
		}
	}
	if usage.JSON.OutputTokensDetails.Valid() && usage.OutputTokensDetails.JSON.ReasoningTokens.Valid() {
		result.ReasoningTokens = int64ptr(usage.OutputTokensDetails.ReasoningTokens)
	}
	return result
}

// openAIRequestOptions 生成两个 OpenAI 适配器共用的 SDK 选项：显式使用配置的 Key 与根地址，
// 禁用 SDK 自带重试，也不继承进程环境里的其他凭据。
func openAIRequestOptions(opts Options) []option.RequestOption {
	return []option.RequestOption{
		option.WithAPIKey(opts.APIKey),
		option.WithBaseURL(opts.BaseURL),
		option.WithMaxRetries(0),
	}
}

// classifyOpenAIStreamError 把 SDK 错误归类成安全的静态错误，不回显原始载荷、Key 或服务端消息。
func classifyOpenAIStreamError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode >= http.StatusBadRequest {
			return httpError(apiErr.StatusCode)
		}
		return &Error{Kind: ErrService, Message: "模型服务在流中返回错误，请检查 Key、额度、模型名或中转站状态"}
	}
	var streamErr *ssestream.StreamError
	if errors.As(err, &streamErr) {
		return &Error{Kind: ErrService, Message: "模型服务在流中返回错误，请检查 Key、额度、模型名或中转站状态"}
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		return protocolError("模型服务返回了无法解析的流式数据")
	}
	return &Error{Kind: ErrNetwork, Message: "模型请求或流式读取失败，请检查网络、代理及 API 协议"}
}

// toolResultText 保留工具结果的错误语义：失败结果用 JSON 封装，成功结果保持原文，
// Responses 与 Chat Completions 都没有原生的错误标记字段。
func toolResultText(result *ToolResult) string {
	if !result.IsError {
		return result.Content
	}
	data, err := json.Marshal(map[string]any{"is_error": true, "content": result.Content})
	if err != nil {
		return result.Content
	}
	return string(data)
}

// messageText 合并消息里的文本块，多个文本块按换行拼接。
func messageText(message Message) string {
	parts := make([]string, 0, len(message.Content))
	for _, block := range message.Content {
		if block.Type == BlockText {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}
