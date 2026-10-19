package agent

import (
	"context"
	"errors"
	"time"

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

type Options struct {
	Client          model.Client
	Tools           Tools
	Model           string
	System          string
	MaxTurns        int
	RequestTimeout  time.Duration
	MaxOutputTokens int
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
	ErrMaxTurns  = errors.New("已达到模型请求次数上限，任务尚未完成")
	ErrMaxTokens = errors.New("模型达到输出上限，响应可能不完整，未执行本次工具调用")
	ErrRefusal   = errors.New("模型拒绝了本次请求")
)

type EventType string

const (
	EventStart        EventType = "start"
	EventRequestStart EventType = "request_start"
	EventModel        EventType = "model_event"
	EventResponse     EventType = "response"
	EventToolStart    EventType = "tool_start"
	EventToolEnd      EventType = "tool_end"
	EventEnd          EventType = "end"
)

// Event 按执行顺序同步交付，观察方不得修改载荷。
// ModelEvent 仅用于流式显示；持久化应使用完整 Response。
type Event struct {
	Type       EventType
	Turn       int
	Message    *model.Message
	ModelEvent *model.Event
	Response   *model.Response
	Call       *model.ToolCall
	ToolResult *model.ToolResult
	Status     Status
	Error      string
}

type Result struct {
	Message model.Message
	Turns   int
	Status  Status
}
