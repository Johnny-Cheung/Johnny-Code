package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

func TestConversationKeepsCompleteHistoryAndResetsTurnBudget(t *testing.T) {
	tools := &testTools{}
	requests := 0
	source := model.Source{API: model.OpenAIResponses, Provider: "test", Model: "model"}
	first := toolResponse("first-tool")
	first.Message.Source = &source
	first.Message.Content = append([]model.ContentBlock{{Type: model.BlockThinking, Replay: &model.Replay{Source: source, Data: json.RawMessage(`{"encrypted_content":"reasoning"}`)}}}, first.Message.Content...)
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		requests++
		switch requests {
		case 1:
			if len(req.Messages) != 1 {
				t.Fatal("第一轮不应有旧历史")
			}
			return first, nil
		case 2:
			return finalResponse(), nil
		case 3:
			if len(req.Messages) != 5 || !reflect.DeepEqual(req.Messages[1], first.Message) || req.Messages[2].Content[0].ToolResult.ToolCallID != "first-tool" || req.Messages[4].Content[0].Text != "接着修改刚才的文件" {
				t.Fatalf("第二轮缺少原样历史：%+v", req.Messages)
			}
			return toolResponse("second-tool"), nil
		case 4:
			if len(req.Tools) != 1 || len(req.Messages) != 7 {
				t.Fatal("第二轮应携带完整工具结果")
			}
			return finalResponse(), nil
		default:
			t.Fatal("不应重试请求")
			return model.Response{}, nil
		}
	})
	conversation := NewConversation(Options{Client: client, Tools: tools, Model: "model", MaxTurns: 2})
	for i, prompt := range []string{"创建文件", "接着修改刚才的文件"} {
		result, err := conversation.Run(context.Background(), prompt, func(event Event) error {
			if event.UserTurn != i+1 {
				t.Fatalf("事件用户轮次未递增：%+v", event)
			}
			return nil
		})
		if err != nil || result.UserTurn != i+1 || result.Turns != 2 || result.Status != StatusCompleted {
			t.Fatalf("逐轮预算或状态错误：%+v %v", result, err)
		}
	}
	if requests != 4 || !reflect.DeepEqual(tools.calls, []string{"first-tool", "second-tool"}) {
		t.Fatalf("执行次数错误：%d %v", requests, tools.calls)
	}
}

func TestConversationDoesNotLoadAnotherConversation(t *testing.T) {
	requests := 0
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		requests++
		if len(req.Messages) != 1 {
			t.Fatal("新会话不应加载已有历史")
		}
		return finalResponse(), nil
	})
	opts := Options{Client: client, Tools: &testTools{}, Model: "model"}
	for i := 0; i < 2; i++ {
		result, err := NewConversation(opts).Run(context.Background(), "新任务", nil)
		if err != nil || result.UserTurn != 1 {
			t.Fatalf("新会话未重置：%+v %v", result, err)
		}
	}
	if requests != 2 {
		t.Fatal("请求次数错误")
	}
}

func TestConversationCanContinueAfterFailedModelResponse(t *testing.T) {
	for _, kind := range []string{"stream", "truncated", "refused", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			requests := 0
			tools := &testTools{}
			client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
				requests++
				if requests == 1 {
					response := toolResponse("not-executed")
					switch kind {
					case "stream":
						return response, errors.New("断流")
					case "truncated":
						response.StopReason = model.StopMaxTokens
						response.Message.Content[0].ToolCall.Arguments = json.RawMessage(`{"path":`)
					case "refused":
						response.StopReason = model.StopRefusal
					case "invalid":
						response.Message.Content[0].ToolCall.Arguments = json.RawMessage(`[]`)
					}
					return response, nil
				}
				if len(req.Messages) != 2 || req.Messages[0].Role != model.RoleUser || req.Messages[1].Role != model.RoleUser {
					t.Fatalf("失败响应不应作为可执行历史：%+v", req.Messages)
				}
				return finalResponse(), nil
			})
			conversation := NewConversation(Options{Client: client, Tools: tools, Model: "model"})
			if _, err := conversation.Run(context.Background(), "第一轮", nil); err == nil {
				t.Fatal("第一轮应失败")
			}
			result, err := conversation.Run(context.Background(), "第二轮", nil)
			if err != nil || result.UserTurn != 2 || requests != 2 || len(tools.calls) != 0 {
				t.Fatalf("失败后无法继续：%+v %v", result, err)
			}
		})
	}
}

func TestConversationBudgetSkippedCallsHaveResultsInNextTurn(t *testing.T) {
	requests := 0
	tools := &testTools{}
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		requests++
		if requests == 1 {
			return toolResponse("skipped"), nil
		}
		if len(req.Messages) != 4 {
			t.Fatalf("预算结束后的历史不完整：%+v", req.Messages)
		}
		result := req.Messages[2].Content[0].ToolResult
		if result.ToolCallID != "skipped" || !result.IsError || !strings.Contains(result.Content, "未执行") {
			t.Fatalf("未执行结果不明确：%+v", result)
		}
		return finalResponse(), nil
	})
	conversation := NewConversation(Options{Client: client, Tools: tools, Model: "model", MaxTurns: 1})
	var skipped int
	if _, err := conversation.Run(context.Background(), "第一轮", func(e Event) error {
		if e.Type == EventToolSkipped {
			skipped++
		}
		return nil
	}); !errors.Is(err, ErrMaxTurns) {
		t.Fatalf("应达到上限：%v", err)
	}
	if _, err := conversation.Run(context.Background(), "下一轮", nil); err != nil {
		t.Fatal(err)
	}
	if skipped != 1 || len(tools.calls) != 0 {
		t.Fatal("未执行调用不应产生工具副作用")
	}
}

func TestConversationKeepsExecutedResultsAfterBatchInterrupted(t *testing.T) {
	requests := 0
	tools := &testTools{}
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		requests++
		if requests == 1 {
			return toolResponse("done", "skipped"), nil
		}
		if len(req.Messages) != 5 {
			t.Fatalf("批中断历史不完整：%+v", req.Messages)
		}
		done, skipped := req.Messages[2].Content[0].ToolResult, req.Messages[3].Content[0].ToolResult
		if done.Content != "done" || done.IsError || skipped.ToolCallID != "skipped" || !skipped.IsError || !strings.Contains(skipped.Content, "未执行") {
			t.Fatalf("已执行/未执行结果错误：%+v %+v", done, skipped)
		}
		return finalResponse(), nil
	})
	conversation := NewConversation(Options{Client: client, Tools: tools, Model: "model"})
	outputErr := errors.New("输出失败")
	if _, err := conversation.Run(context.Background(), "第一轮", func(e Event) error {
		if e.Type == EventToolEnd {
			return outputErr
		}
		return nil
	}); !errors.Is(err, outputErr) {
		t.Fatal("应保留原始中断错误")
	}
	if _, err := conversation.Run(context.Background(), "下一轮", nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tools.calls, []string{"done"}) {
		t.Fatalf("不应重跑工具：%v", tools.calls)
	}
}

func TestConversationRejectsToolIDsAlreadyInHistory(t *testing.T) {
	requests := 0
	tools := &testTools{}
	client := clientFunc(func(context.Context, model.Request, func(model.Event) error) (model.Response, error) {
		requests++
		if requests == 2 {
			return finalResponse(), nil
		}
		return toolResponse("same-id"), nil
	})
	conversation := NewConversation(Options{Client: client, Tools: tools, Model: "model"})
	if _, err := conversation.Run(context.Background(), "第一轮", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.Run(context.Background(), "第二轮", nil); err == nil {
		t.Fatal("不能重放已存在的调用 ID")
	}
	if len(tools.calls) != 1 {
		t.Fatalf("重复调用 ID 已产生额外副作用：%v", tools.calls)
	}
}
