package model

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func NewClient(opts Options) (Client, error) {
	if opts.API == "" {
		opts.API = AnthropicMessages
	}
	if !opts.API.Valid() {
		return nil, &Error{Kind: ErrUnsupported, Message: "不支持的模型 API 协议"}
	}
	opts.APIKey = strings.TrimSpace(opts.APIKey)
	if opts.APIKey == "" {
		return nil, invalidRequest("模型 API Key 不能为空")
	}
	u, err := url.Parse(strings.TrimSpace(opts.BaseURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, invalidRequest("模型地址必须是有效的 http 或 https API 根地址")
	}
	path := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/messages", "/responses", "/chat/completions"} {
		if strings.HasSuffix(path, suffix) {
			return nil, invalidRequest("模型地址请去掉末尾接口路径，保留 API 根地址")
		}
	}
	if opts.API == AnthropicMessages {
		path = strings.TrimSuffix(path, "/v1")
	} else if !strings.HasSuffix(path, "/v1") {
		path += "/v1"
	}
	u.Path, u.RawPath = path+"/", ""
	opts.BaseURL = u.String()
	var client Client
	switch opts.API {
	case AnthropicMessages:
		client = &anthropicClient{options: opts}
	case OpenAIResponses:
		client = &openAIResponsesClient{options: opts}
	case OpenAIChatCompletions:
		client = &openAIChatClient{options: opts}
	}
	return &validatedClient{client: client, options: opts}, nil
}

type validatedClient struct {
	client  Client
	options Options
}

func (c *validatedClient) Stream(ctx context.Context, req Request, emit func(Event) error) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if req.MaxOutputTokens == 0 {
		req.MaxOutputTokens = 4096
	}
	if err := validateRequest(req, c.options); err != nil {
		return Response{}, err
	}
	return c.client.Stream(ctx, req, emit)
}

func sourceFor(opts Options, model string) Source {
	return Source{API: opts.API, Provider: strings.TrimRight(opts.BaseURL, "/"), Model: model}
}

func invalidRequest(message string) error { return &Error{Kind: ErrInvalidRequest, Message: message} }

func validateRequest(req Request, opts Options) error {
	if strings.TrimSpace(req.Model) == "" || req.MaxOutputTokens < 1 || len(req.Messages) == 0 {
		return invalidRequest("模型名、消息列表和正数输出上限必须提供")
	}
	names := make(map[string]bool)
	for _, tool := range req.Tools {
		if tool.Name == "" || names[tool.Name] || !jsonObject(tool.InputSchema) {
			return invalidRequest("工具名称必须唯一且非空，输入 schema 必须是 JSON 对象")
		}
		names[tool.Name] = true
	}
	pending, seen := make(map[string]bool), make(map[string]bool)
	for _, msg := range req.Messages {
		if msg.Role != RoleUser && msg.Role != RoleAssistant && msg.Role != RoleTool {
			return invalidRequest("不支持的消息角色")
		}
		if len(msg.Content) == 0 {
			return invalidRequest("消息内容不能为空")
		}
		if len(pending) > 0 && msg.Role != RoleTool {
			return invalidRequest("工具调用之后必须先提供对应工具结果")
		}
		for _, block := range msg.Content {
			if block.Replay != nil {
				if block.Replay.Source != sourceFor(opts, req.Model) {
					return &Error{Kind: ErrUnsupported, Message: "暂不支持跨服务、协议或模型重放专用数据，请使用新的消息历史"}
				}
				if !jsonObject(block.Replay.Data) {
					return invalidRequest("协议续接数据必须是 JSON 对象")
				}
			}
			switch block.Type {
			case BlockText:
				if msg.Role == RoleTool || block.ToolCall != nil || block.ToolResult != nil {
					return invalidRequest("文本块与消息角色或载荷不匹配")
				}
				if block.Replay != nil && (msg.Role != RoleAssistant || opts.API != OpenAIResponses) {
					return &Error{Kind: ErrUnsupported, Message: "此协议不支持文本块的专用续接数据"}
				}
			case BlockThinking:
				if msg.Role != RoleAssistant || block.ToolCall != nil || block.ToolResult != nil || block.Replay == nil {
					return invalidRequest("思考块只能重放带来源数据的 assistant 内容")
				}
			case BlockToolCall:
				call := block.ToolCall
				if msg.Role != RoleAssistant || call == nil || block.ToolResult != nil || block.Text != "" || call.ID == "" || call.Name == "" || !jsonObject(call.Arguments) || seen[call.ID] {
					return invalidRequest("工具调用必须包含唯一 ID、名称和 JSON 对象参数")
				}
				if block.Replay != nil && opts.API != OpenAIResponses {
					return &Error{Kind: ErrUnsupported, Message: "此协议不支持工具调用的专用续接数据"}
				}
				pending[call.ID], seen[call.ID] = true, true
			case BlockToolResult:
				result := block.ToolResult
				if msg.Role != RoleTool || result == nil || block.ToolCall != nil || block.Text != "" || block.Replay != nil || !pending[result.ToolCallID] {
					return invalidRequest("工具结果必须对应尚未返回结果的工具调用")
				}
				delete(pending, result.ToolCallID)
			default:
				return &Error{Kind: ErrUnsupported, Message: "不支持的消息内容块"}
			}
		}
	}
	if len(pending) > 0 {
		return invalidRequest("消息历史缺少工具调用结果")
	}
	return nil
}

func httpError(status int) error {
	kind := ErrService
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = ErrAuthentication
	case status == http.StatusTooManyRequests:
		kind = ErrRateLimit
	case status >= 400 && status < 500:
		kind = ErrInvalidRequest
	}
	return &Error{Kind: kind, StatusCode: status, Message: fmt.Sprintf("模型服务返回 HTTP %d，请检查 Key、URL、模型名或服务状态", status)}
}
