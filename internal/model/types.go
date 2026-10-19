package model

import (
	"context"
	"encoding/json"
)

type API string

const (
	AnthropicMessages     API = "anthropic-messages"
	OpenAIResponses       API = "openai-responses"
	OpenAIChatCompletions API = "openai-chat-completions"
)

func (a API) Valid() bool {
	return a == AnthropicMessages || a == OpenAIResponses || a == OpenAIChatCompletions
}

type Client interface {
	Stream(context.Context, Request, func(Event) error) (Response, error)
}

type Options struct {
	API     API
	BaseURL string
	APIKey  string
}

type Request struct {
	Model           string
	System          string
	Messages        []Message
	Tools           []ToolDefinition
	MaxOutputTokens int
}

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Source struct {
	API      API    `json:"api"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
	Source  *Source        `json:"source,omitempty"`
}

type BlockType string

const (
	BlockText       BlockType = "text"
	BlockThinking   BlockType = "thinking"
	BlockToolCall   BlockType = "tool_call"
	BlockToolResult BlockType = "tool_result"
)

// Replay 保存同一服务、协议和模型续接需要的数据，不用于终端显示。
// 任务诊断记录会保存这些数据，但不会从旧记录恢复会话或生成记忆。
type Replay struct {
	Source Source          `json:"source"`
	Data   json.RawMessage `json:"data"`
}

type ContentBlock struct {
	Type       BlockType   `json:"type"`
	Text       string      `json:"text,omitempty"`
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	Replay     *Replay     `json:"replay,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error,omitempty"`
}

type ToolDefinition struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

type EventType string

const (
	EventBlockStart EventType = "block_start"
	EventBlockDelta EventType = "block_delta"
	EventBlockEnd   EventType = "block_end"
)

type Event struct {
	Type       EventType
	BlockIndex int
	Block      ContentBlock
	Delta      string
}

type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopRefusal   StopReason = "refusal"
)

// InputTokens 包含缓存输入，OutputTokens 包含推理输出；分项不能再加到总数。
// nil 表示服务没有提供该统计。
type Usage struct {
	InputTokens      *int64
	OutputTokens     *int64
	CacheReadTokens  *int64
	CacheWriteTokens *int64
	ReasoningTokens  *int64
}

type Response struct {
	Message    Message
	StopReason StopReason
	Usage      Usage
}

type ErrorKind string

const (
	ErrInvalidRequest ErrorKind = "invalid_request"
	ErrAuthentication ErrorKind = "authentication"
	ErrRateLimit      ErrorKind = "rate_limit"
	ErrService        ErrorKind = "service"
	ErrNetwork        ErrorKind = "network"
	ErrProtocol       ErrorKind = "protocol"
	ErrUnsupported    ErrorKind = "unsupported"
)

type Error struct {
	Kind       ErrorKind
	StatusCode int
	Message    string
}

func (e *Error) Error() string { return e.Message }
