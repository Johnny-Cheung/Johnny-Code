package agent

import (
	"context"
	"errors"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

const (
	DefaultMaxTurns       = 50
	DefaultRequestTimeout = 5 * time.Minute
)

type Tools interface {
	Definitions() []model.ToolDefinition
	Execute(context.Context, model.ToolCall) (tool.Result, error)
}

// PinnedContext 返回固定注入的规则和记忆索引，以及不阻断任务的警告。
// 运行时只在会话启动和压缩后调用，写入路径不会触发前缀重建。
type PinnedContext func() (string, string)

type Options struct {
	Client          model.Client
	Tools           Tools
	Model           string
	System          string
	InteractionMode string
	MaxTurns        int
	RequestTimeout  time.Duration
	MaxOutputTokens int
	// ContextView 为 nil 时使用默认窗口创建请求视图，便于直接使用 Agent 的调用方。
	ContextView *contextview.Manager
	// Pinned 为 nil 时不注入长期规则和记忆索引。
	Pinned PinnedContext
}

type Status string

const (
	StatusCompleted      Status = "completed"
	StatusCanceled       Status = "canceled"
	StatusRequestTimeout Status = "request_timeout"
	StatusMaxTurns       Status = "max_turns"
	StatusMaxTokens      Status = "max_tokens"
	StatusRefusal        Status = "refusal"
	StatusFailed         Status = "failed"
)

var (
	ErrMaxTurns        = errors.New("已达到模型请求次数上限，任务尚未完成")
	ErrMaxTokens       = errors.New("模型达到输出上限，响应可能不完整，未执行本次工具调用")
	ErrRefusal         = errors.New("模型拒绝了本次请求")
	ErrContextExceeded = errors.New("上下文压缩后仍超过可用输入预算")
)

type EventType string

const (
	EventStart        EventType = "start"
	EventRequestStart EventType = "request_start"
	EventModel        EventType = "model_event"
	EventResponse     EventType = "response"
	EventToolStart    EventType = "tool_start"
	EventToolEnd      EventType = "tool_end"
	EventToolSkipped  EventType = "tool_skipped"
	EventNotice       EventType = "notice"
	EventSummary      EventType = "summary"
	EventEnd          EventType = "end"
)

// SummaryInfo 描述一次极限态摘要的结果，便于展示和诊断记录。
type SummaryInfo struct {
	Text           string
	Before         int
	After          int
	CoveredThrough int
	Note           bool
}

// Event 按执行顺序同步交付，观察方不得修改载荷。
// ModelEvent 仅用于流式显示；持久化应使用完整 Response。
type Event struct {
	Type       EventType
	UserTurn   int
	Turn       int
	Message    *model.Message
	ModelEvent *model.Event
	Response   *model.Response
	Call       *model.ToolCall
	ToolResult *model.ToolResult
	Artifact   string
	Notice     string
	Summary    *SummaryInfo
	Status     Status
	Error      string
}

type Result struct {
	Message  model.Message
	UserTurn int
	Turns    int
	Status   Status
}
