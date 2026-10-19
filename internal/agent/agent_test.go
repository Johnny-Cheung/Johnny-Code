package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

type clientFunc func(context.Context, model.Request, func(model.Event) error) (model.Response, error)

func (f clientFunc) Stream(ctx context.Context, req model.Request, emit func(model.Event) error) (model.Response, error) {
	return f(ctx, req, emit)
}

type testTools struct {
	calls   []string
	execute func(context.Context, model.ToolCall) (tool.Result, error)
}

func (t *testTools) Definitions() []model.ToolDefinition {
	return []model.ToolDefinition{{Name: "echo", Description: "test", InputSchema: json.RawMessage(`{"type":"object"}`)}}
}

func (t *testTools) Execute(ctx context.Context, call model.ToolCall) (tool.Result, error) {
	t.calls = append(t.calls, call.ID)
	if t.execute != nil {
		return t.execute(ctx, call)
	}
	return tool.Result{Content: call.ID}, nil
}

func toolResponse(ids ...string) model.Response {
	message := model.Message{Role: model.RoleAssistant}
	for _, id := range ids {
		message.Content = append(message.Content, model.ContentBlock{Type: model.BlockToolCall, ToolCall: &model.ToolCall{ID: id, Name: "echo", Arguments: json.RawMessage(`{}`)}})
	}
	return model.Response{Message: message, StopReason: model.StopToolUse}
}

func finalResponse() model.Response {
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{Type: model.BlockText, Text: "完成"}}}, StopReason: model.StopEndTurn}
}

func TestRunKeepsHistoryReplayAndSerialErrorResults(t *testing.T) {
	tools := &testTools{execute: func(ctx context.Context, call model.ToolCall) (tool.Result, error) {
		if _, ok := ctx.Deadline(); ok || ctx.Err() != nil {
			t.Fatal("工具不应继承模型请求的截止时间或被释放的子 context")
		}
		if call.ID == "one" {
			return tool.Result{Content: "partial"}, context.DeadlineExceeded
		}
		return tool.Result{Content: "ok"}, nil
	}}
	first := toolResponse("one", "two")
	source := model.Source{API: model.OpenAIResponses, Provider: "test", Model: "model"}
	first.Message.Source = &source
	first.Message.Content = append([]model.ContentBlock{{Type: model.BlockThinking, Text: "thinking", Replay: &model.Replay{Source: source, Data: json.RawMessage(`{"encrypted_content":"secret"}`)}}}, first.Message.Content...)
	requests := 0
	var events []EventType
	client := clientFunc(func(ctx context.Context, req model.Request, emit func(model.Event) error) (model.Response, error) {
		requests++
		if req.Model != "model" || req.System != "system" || len(req.Tools) != 1 || req.MaxOutputTokens != 4096 {
			t.Fatalf("请求配置丢失：%+v", req)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("每次模型请求应有超时")
		}
		if requests == 1 {
			return first, emit(model.Event{Type: model.EventBlockDelta, Block: model.ContentBlock{Type: model.BlockText}, Delta: "先执行"})
		}
		if len(req.Messages) != 4 || !reflect.DeepEqual(req.Messages[1], first.Message) {
			t.Fatalf("未保留完整消息和 Replay：%+v", req.Messages)
		}
		for i, id := range []string{"one", "two"} {
			msg := req.Messages[i+2]
			got := msg.Content[0].ToolResult
			if msg.Role != model.RoleTool || got.ToolCallID != id || got.IsError != (i == 0) {
				t.Fatalf("工具结果关联错误：%+v", msg)
			}
		}
		return finalResponse(), nil
	})
	result, err := Run(context.Background(), Options{Client: client, Tools: tools, Model: "model", System: "system"}, "task", func(e Event) error {
		events = append(events, e.Type)
		return nil
	})
	if err != nil || result.Status != StatusCompleted || result.Turns != 2 || !reflect.DeepEqual(tools.calls, []string{"one", "two"}) {
		t.Fatalf("循环错误：%+v %v calls=%v", result, err, tools.calls)
	}
	want := []EventType{EventStart, EventRequestStart, EventModel, EventResponse, EventToolStart, EventToolEnd, EventToolStart, EventToolEnd, EventRequestStart, EventResponse, EventEnd}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("事件顺序错误：%v", events)
	}
}

func TestRunStopsBeforeToolsOnIncompleteOrFailedResponses(t *testing.T) {
	transportErr := errors.New("transport failed")
	for _, tc := range []struct {
		name     string
		response model.Response
		err      error
		status   Status
	}{
		{"truncated complete call", func() model.Response { r := toolResponse("one"); r.StopReason = model.StopMaxTokens; return r }(), nil, StatusMaxTokens},
		{"truncated arguments", func() model.Response {
			r := toolResponse("one")
			r.StopReason = model.StopMaxTokens
			r.Message.Content[0].ToolCall.Arguments = json.RawMessage(`{"path":`)
			return r
		}(), nil, StatusMaxTokens},
		{"refusal", func() model.Response { r := toolResponse("one"); r.StopReason = model.StopRefusal; return r }(), nil, StatusRefusal},
		{"stream failure", toolResponse("one"), transportErr, StatusFailed},
		{"unknown stop", func() model.Response { r := toolResponse("one"); r.StopReason = "unknown"; return r }(), nil, StatusFailed},
		{"end with calls", func() model.Response { r := toolResponse("one"); r.StopReason = model.StopEndTurn; return r }(), nil, StatusFailed},
		{"duplicate id", toolResponse("one", "one"), nil, StatusFailed},
		{"missing calls", toolResponse(), nil, StatusFailed},
		{"invalid arguments", func() model.Response {
			r := toolResponse("one")
			r.Message.Content[0].ToolCall.Arguments = json.RawMessage(`[]`)
			return r
		}(), nil, StatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := &testTools{}
			requests := 0
			var end Event
			client := clientFunc(func(context.Context, model.Request, func(model.Event) error) (model.Response, error) {
				requests++
				return tc.response, tc.err
			})
			result, err := Run(context.Background(), Options{Client: client, Tools: tools, Model: "model"}, "task", func(e Event) error {
				if e.Type == EventEnd {
					end = e
				}
				return nil
			})
			if err == nil || result.Status != tc.status || requests != 1 || len(tools.calls) != 0 || end.Status != tc.status || end.Error == "" {
				t.Fatalf("停止状态错误：%+v %v requests=%d calls=%v end=%+v", result, err, requests, tools.calls, end)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatal("原始错误链丢失")
			}
		})
	}
}

func TestRunTurnBudgetCountsRequestsAndAllowsFinalAnswer(t *testing.T) {
	for _, lastIsFinal := range []bool{false, true} {
		tools := &testTools{}
		requests := 0
		client := clientFunc(func(context.Context, model.Request, func(model.Event) error) (model.Response, error) {
			requests++
			if requests == 1 {
				return toolResponse("one", "two"), nil
			}
			if lastIsFinal {
				return finalResponse(), nil
			}
			return toolResponse("three"), nil
		})
		result, err := Run(context.Background(), Options{Client: client, Tools: tools, Model: "model", MaxTurns: 2}, "task", nil)
		if requests != 2 || len(tools.calls) != 2 || result.Turns != 2 {
			t.Fatalf("预算计数错误：%+v %v calls=%v", result, err, tools.calls)
		}
		if lastIsFinal && err != nil || !lastIsFinal && !errors.Is(err, ErrMaxTurns) {
			t.Fatalf("最后一轮处理错误：%+v %v", result, err)
		}
	}
	tools := &testTools{}
	client := clientFunc(func(context.Context, model.Request, func(model.Event) error) (model.Response, error) {
		return toolResponse("one"), nil
	})
	_, err := Run(context.Background(), Options{Client: client, Tools: tools, Model: "model", MaxTurns: 1}, "task", nil)
	if !errors.Is(err, ErrMaxTurns) || len(tools.calls) != 0 {
		t.Fatalf("初次请求也应计入预算：%v", err)
	}
}

func TestRunCancellationStopsRemainingBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tools := &testTools{execute: func(context.Context, model.ToolCall) (tool.Result, error) {
		cancel()
		return tool.Result{Content: "partial"}, context.Canceled
	}}
	requests := 0
	client := clientFunc(func(context.Context, model.Request, func(model.Event) error) (model.Response, error) {
		requests++
		return toolResponse("one", "two"), nil
	})
	result, err := Run(ctx, Options{Client: client, Tools: tools, Model: "model"}, "task", nil)
	if !errors.Is(err, context.Canceled) || result.Status != StatusCanceled || requests != 1 || !reflect.DeepEqual(tools.calls, []string{"one"}) {
		t.Fatalf("取消后仍继续调度：%+v %v calls=%v", result, err, tools.calls)
	}
}

func TestRunRequestTimeoutAndObserverFailure(t *testing.T) {
	tools := &testTools{}
	client := clientFunc(func(ctx context.Context, _ model.Request, _ func(model.Event) error) (model.Response, error) {
		<-ctx.Done()
		return model.Response{}, ctx.Err()
	})
	result, err := Run(context.Background(), Options{Client: client, Tools: tools, Model: "model", RequestTimeout: 10 * time.Millisecond}, "task", nil)
	if !errors.Is(err, context.DeadlineExceeded) || result.Status != StatusRequestTimeout {
		t.Fatalf("请求超时丢失：%+v %v", result, err)
	}
	observerErr := errors.New("output failed")
	client = clientFunc(func(context.Context, model.Request, func(model.Event) error) (model.Response, error) {
		return toolResponse("one"), nil
	})
	_, err = Run(context.Background(), Options{Client: client, Tools: tools, Model: "model"}, "task", func(e Event) error {
		if e.Type == EventToolStart {
			return observerErr
		}
		return nil
	})
	if !errors.Is(err, observerErr) || len(tools.calls) != 0 {
		t.Fatalf("观察者失败后不应执行工具：%v %v", err, tools.calls)
	}
}

func TestRunUnknownToolReturnsErrorToModel(t *testing.T) {
	registry, err := tool.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		requests++
		if requests == 1 {
			return toolResponse("unknown"), nil
		}
		if !req.Messages[2].Content[0].ToolResult.IsError || !strings.Contains(req.Messages[2].Content[0].ToolResult.Content, "未知工具") {
			t.Fatal("未知工具未回传模型")
		}
		return finalResponse(), nil
	})
	if _, err := Run(context.Background(), Options{Client: client, Tools: registry, Model: "model"}, "task", nil); err != nil {
		t.Fatal(err)
	}
}
