package model

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type anthropicClient struct{ options Options }

func (a *anthropicClient) Stream(ctx context.Context, req Request, emit func(Event) error) (Response, error) {
	c := newCollector(sourceFor(a.options, req.Model), emit)
	params := anthropic.MessageNewParams{Model: anthropic.Model(req.Model), MaxTokens: int64(req.MaxOutputTokens)}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	for _, msg := range req.Messages {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(msg.Content))
		for _, b := range msg.Content {
			var block anthropic.ContentBlockParamUnion
			switch b.Type {
			case BlockText:
				block = anthropic.NewTextBlock(b.Text)
			case BlockThinking:
				if err := json.Unmarshal(b.Replay.Data, &block); err != nil {
					return c.response, invalidRequest("思考续接数据无效")
				}
			case BlockToolCall:
				block = anthropic.NewToolUseBlock(b.ToolCall.ID, b.ToolCall.Arguments, b.ToolCall.Name)
			case BlockToolResult:
				block = anthropic.NewToolResultBlock(b.ToolResult.ToolCallID, b.ToolResult.Content, b.ToolResult.IsError)
			}
			blocks = append(blocks, block)
		}
		if msg.Role == RoleAssistant {
			params.Messages = append(params.Messages, anthropic.NewAssistantMessage(blocks...))
		} else {
			params.Messages = append(params.Messages, anthropic.NewUserMessage(blocks...))
		}
	}
	for _, tool := range req.Tools {
		var schema anthropic.ToolInputSchemaParam
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			return c.response, invalidRequest("工具 schema 无效")
		}
		params.Tools = append(params.Tools, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{Name: tool.Name, Description: anthropic.String(tool.Description), InputSchema: schema}})
	}
	client := anthropic.NewClient(option.WithoutEnvironmentDefaults(), option.WithAPIKey(""), option.WithHeaderDel("X-Api-Key"), option.WithAuthToken(a.options.APIKey), option.WithBaseURL(a.options.BaseURL), option.WithMaxRetries(0))
	stream := client.Messages.NewStreaming(ctx, params)
	defer func() { _ = stream.Close() }()
	indices := make(map[int]int)
	signatures := make(map[int]string)
	var reason StopReason
	var uncached *int64
	stopped := false
	for stream.Next() {
		var event struct {
			Type         string          `json:"type"`
			Index        int             `json:"index"`
			ContentBlock json.RawMessage `json:"content_block"`
			Message      struct {
				Usage map[string]json.RawMessage `json:"usage"`
			} `json:"message"`
			Usage map[string]json.RawMessage `json:"usage"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(stream.Current().RawJSON()), &event); err != nil {
			return c.response, protocolError("Anthropic 流式事件格式无效")
		}
		switch event.Type {
		case "message_start":
			if err := updateAnthropicUsage(&c.response.Usage, &uncached, event.Message.Usage); err != nil {
				return c.response, err
			}
		case "content_block_start":
			if _, exists := indices[event.Index]; exists {
				return c.response, protocolError("模型重复开始同一个内容块")
			}
			var raw struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				Thinking  string          `json:"thinking"`
				Signature string          `json:"signature"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Input     json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(event.ContentBlock, &raw); err != nil {
				return c.response, protocolError("模型内容块格式无效")
			}
			block := ContentBlock{}
			switch raw.Type {
			case "text":
				block.Type, block.Text = BlockText, raw.Text
			case "thinking":
				block.Type, block.Text = BlockThinking, raw.Thinking
			case "redacted_thinking":
				block.Type = BlockThinking
			case "tool_use":
				block.Type, block.ToolCall = BlockToolCall, &ToolCall{ID: raw.ID, Name: raw.Name, Arguments: raw.Input}
			default:
				return c.response, &Error{Kind: ErrUnsupported, Message: "模型返回了尚不支持的 Anthropic 内容块"}
			}
			if block.Type == BlockThinking {
				block.Replay = &Replay{Source: *c.response.Message.Source, Data: append(json.RawMessage(nil), event.ContentBlock...)}
			}
			index, err := c.start(block)
			if err != nil {
				return c.response, err
			}
			indices[event.Index], signatures[index] = index, raw.Signature
		case "content_block_delta":
			index, ok := indices[event.Index]
			if !ok {
				return c.response, protocolError("模型在内容块开始前发送了增量")
			}
			var err error
			switch event.Delta.Type {
			case "text_delta":
				if c.response.Message.Content[index].Type != BlockText {
					return c.response, protocolError("模型文本增量与内容块类型不一致")
				}
				err = c.delta(index, event.Delta.Text)
			case "thinking_delta":
				if c.response.Message.Content[index].Type != BlockThinking {
					return c.response, protocolError("模型思考增量与内容块类型不一致")
				}
				err = c.delta(index, event.Delta.Thinking)
			case "input_json_delta":
				if c.response.Message.Content[index].Type != BlockToolCall {
					return c.response, protocolError("模型工具增量与内容块类型不一致")
				}
				err = c.delta(index, event.Delta.PartialJSON)
			case "signature_delta":
				if c.response.Message.Content[index].Type != BlockThinking || c.ended[index] {
					return c.response, protocolError("模型签名增量顺序或类型无效")
				}
				signatures[index] += event.Delta.Signature
			default:
				return c.response, &Error{Kind: ErrUnsupported, Message: "模型返回了尚不支持的 Anthropic 增量事件"}
			}
			if err != nil {
				return c.response, err
			}
		case "content_block_stop":
			index, ok := indices[event.Index]
			if !ok {
				return c.response, protocolError("模型结束了尚未开始的内容块")
			}
			block := &c.response.Message.Content[index]
			if block.Type == BlockThinking {
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(block.Replay.Data, &raw); err != nil {
					return c.response, protocolError("思考数据格式无效")
				}
				if string(raw["type"]) == `"thinking"` {
					data, _ := json.Marshal(map[string]string{"type": "thinking", "thinking": block.Text, "signature": signatures[index]})
					block.Replay.Data = data
				}
			}
			if block.Type != BlockToolCall || toolCallComplete(block) {
				if err := c.end(index); err != nil {
					return c.response, err
				}
			}
		case "message_delta":
			if err := updateAnthropicUsage(&c.response.Usage, &uncached, event.Usage); err != nil {
				return c.response, err
			}
			if event.Delta.StopReason != "" {
				switch event.Delta.StopReason {
				case "end_turn", "stop_sequence":
					reason = StopEndTurn
				case "tool_use":
					reason = StopToolUse
				case "max_tokens", "model_context_window_exceeded":
					reason = StopMaxTokens
				case "refusal":
					reason = StopRefusal
				default:
					return c.response, protocolError("模型返回了尚不支持的 Anthropic 结束原因")
				}
			}
		case "message_stop":
			stopped = true
		case "ping":
		default:
			return c.response, protocolError("模型返回了无法识别的 Anthropic 流式事件")
		}
		if stopped {
			break
		}
	}
	if ctx.Err() != nil {
		return c.response, ctx.Err()
	}
	if err := stream.Err(); err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			if apiErr.StatusCode == 200 {
				return c.response, &Error{Kind: ErrService, Message: "模型服务在流中返回错误，请检查 Key、额度、模型名或中转站状态"}
			}
			return c.response, httpError(apiErr.StatusCode)
		}
		return c.response, &Error{Kind: ErrNetwork, Message: "模型请求或流式读取失败，请检查网络、代理及 API 协议"}
	}
	if !stopped {
		return c.response, protocolError("流式连接在模型完成回复前结束，请重试或检查中转站")
	}
	if reason == StopMaxTokens {
		if err := endOpenBlocks(c, true); err != nil {
			return c.response, err
		}
	}
	if err := c.complete(reason); err != nil {
		return c.response, err
	}
	return c.response, nil
}

func updateAnthropicUsage(usage *Usage, uncached **int64, raw map[string]json.RawMessage) error {
	for name, dest := range map[string]**int64{"input_tokens": uncached, "output_tokens": &usage.OutputTokens, "cache_read_input_tokens": &usage.CacheReadTokens, "cache_creation_input_tokens": &usage.CacheWriteTokens} {
		data, ok := raw[name]
		if !ok || string(data) == "null" {
			continue
		}
		var value int64
		if err := json.Unmarshal(data, &value); err != nil || value < 0 {
			return protocolError("模型 token 用量格式无效")
		}
		*dest = int64ptr(value)
	}
	if data, ok := raw["output_tokens_details"]; ok && string(data) != "null" {
		var details struct {
			ThinkingTokens *int64 `json:"thinking_tokens"`
		}
		if err := json.Unmarshal(data, &details); err != nil || (details.ThinkingTokens != nil && *details.ThinkingTokens < 0) {
			return protocolError("模型推理 token 用量格式无效")
		}
		if details.ThinkingTokens != nil {
			usage.ReasoningTokens = details.ThinkingTokens
		}
	}
	if *uncached != nil {
		total := **uncached
		if usage.CacheReadTokens != nil {
			total += *usage.CacheReadTokens
		}
		if usage.CacheWriteTokens != nil {
			total += *usage.CacheWriteTokens
		}
		usage.InputTokens = int64ptr(total)
	}
	return nil
}
