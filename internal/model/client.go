package model

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultMaxRetries   = 3
	DefaultRetryBackoff = time.Second
	maxRetryBackoff     = 8 * time.Second
	// 服务端给的 Retry-After 可能很长；等待过久会让用户以为程序卡死。
	maxRetryAfterWait = 30 * time.Second
)

func NewClient(opts Options) (Client, error) {
	if opts.API == "" {
		opts.API = AnthropicMessages
	}
	if !opts.API.Valid() {
		return nil, &Error{Kind: ErrUnsupported, Message: "不支持的模型 API 协议"}
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = DefaultMaxRetries
	}
	if opts.MaxRetries > 10 {
		opts.MaxRetries = 10
	}
	if opts.RetryBackoff <= 0 {
		opts.RetryBackoff = DefaultRetryBackoff
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
		// 与对话层的默认输出预留保持一致；调用方通常已经显式传入。
		req.MaxOutputTokens = 8192
	}
	if err := validateRequest(req, c.options); err != nil {
		return Response{}, err
	}
	return c.streamWithRetry(ctx, req, emit)
}

// streamWithRetry 在尚未向调用方交付任何流事件时，对限流、服务端和网络错误
// 做指数退避重试。已经显示过部分回复的流不重试，避免终端出现重复内容。
func (c *validatedClient) streamWithRetry(ctx context.Context, req Request, emit func(Event) error) (Response, error) {
	retries := c.options.MaxRetries
	if retries < 0 {
		retries = 0
	}
	var last Response
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			if err := waitRetry(ctx, retryDelay(c.options.RetryBackoff, lastErr, attempt)); err != nil {
				return last, err
			}
		}
		delivered := false
		wrapped := func(event Event) error {
			delivered = true
			if emit == nil {
				return nil
			}
			return emit(event)
		}
		response, err := c.client.Stream(ctx, req, wrapped)
		if err == nil {
			return response, nil
		}
		last, lastErr = response, err
		if delivered || ctx.Err() != nil || !retryable(err) {
			return response, err
		}
	}
	return last, lastErr
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryDelay(backoff time.Duration, err error, attempt int) time.Duration {
	var modelErr *Error
	if errors.As(err, &modelErr) && modelErr.RetryAfter > 0 {
		if modelErr.RetryAfter > maxRetryAfterWait {
			return maxRetryAfterWait
		}
		return modelErr.RetryAfter
	}
	delay := backoff
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= maxRetryBackoff {
			return maxRetryBackoff
		}
	}
	return delay
}

func retryable(err error) bool {
	var modelErr *Error
	if !errors.As(err, &modelErr) {
		return false
	}
	switch modelErr.Kind {
	case ErrRateLimit, ErrNetwork:
		return true
	case ErrService:
		return modelErr.StatusCode == 0 || modelErr.StatusCode >= http.StatusInternalServerError
	default:
		return false
	}
}

// retryAfterDuration 读取服务端 Retry-After 响应头；非法值按 0 处理。
func retryAfterDuration(response *http.Response) time.Duration {
	if response == nil {
		return 0
	}
	value := strings.TrimSpace(response.Header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
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
	return httpErrorAfter(status, 0)
}

func httpErrorAfter(status int, retryAfter time.Duration) error {
	kind := ErrService
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = ErrAuthentication
	case status == http.StatusTooManyRequests:
		kind = ErrRateLimit
	case status >= 400 && status < 500:
		kind = ErrInvalidRequest
	}
	return &Error{Kind: kind, StatusCode: status, RetryAfter: retryAfter, Message: fmt.Sprintf("模型服务返回 HTTP %d，请检查 Key、URL、模型名或服务状态", status)}
}
