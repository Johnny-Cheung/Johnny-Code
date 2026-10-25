package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func checkToolPairs(t *testing.T, label string, messages []model.Message) {
	t.Helper()
	pending := map[string]bool{}
	for i, message := range messages {
		if len(pending) > 0 && message.Role != model.RoleTool {
			t.Fatalf("%s：第 %d 条消息在工具结果之前出现（%s）", label, i, message.Role)
		}
		for _, block := range message.Content {
			if block.ToolCall != nil {
				pending[block.ToolCall.ID] = true
			}
			if block.ToolResult != nil {
				if !pending[block.ToolResult.ToolCallID] {
					t.Fatalf("%s：第 %d 条消息存在孤立工具结果 %s", label, i, block.ToolResult.ToolCallID)
				}
				delete(pending, block.ToolResult.ToolCallID)
			}
		}
	}
	if len(pending) > 0 {
		t.Fatalf("%s：存在悬空工具调用 %v", label, pending)
	}
}

func TestMidTurnCompactionKeepsToolPairs(t *testing.T) {
	manager := contextview.NewManager(contextview.Config{Budget: contextview.NewBudget(40_000, 4096)})
	for turn := 1; turn <= 7; turn++ {
		id := fmt.Sprintf("call-%d", turn)
		manager.BeginTurn(turn, textMessage(model.RoleUser, fmt.Sprintf("task-%d", turn)))
		manager.AppendAssistant(toolCallMessage(id))
		manager.AppendToolResult(turn, model.ToolCall{Name: "bash"}, model.ToolResult{
			ToolCallID: id,
			Content:    fmt.Sprintf("TURN-%d-", turn) + string(make([]byte, 12_000)),
		})
		manager.AppendAssistant(textMessage(model.RoleAssistant, fmt.Sprintf("done-%d", turn)))
		manager.FinishTurn()
	}
	requests, summaries := 0, 0
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		requests++
		checkToolPairs(t, fmt.Sprintf("request-%d", requests), req.Messages)
		if req.System == summarySystemPrompt {
			summaries++
			return model.Response{Message: textMessage(model.RoleAssistant, "<summary>摘要</summary>"), StopReason: model.StopEndTurn}, nil
		}
		if requests < 9 {
			return model.Response{Message: toolCallMessage(fmt.Sprintf("loop-%d", requests)), StopReason: model.StopToolUse}, nil
		}
		return finalResponse(), nil
	})
	tools := &testTools{execute: func(context.Context, model.ToolCall) (tool.Result, error) {
		return tool.Result{Content: string(make([]byte, 12_000))}, nil
	}}
	conversation := NewConversation(Options{Client: client, Tools: tools, Model: "model", ContextView: manager, MaxTurns: 30})
	if _, err := conversation.Run(context.Background(), "new-task", nil); err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	if summaries == 0 {
		t.Fatal("应触发轮内极限态压缩")
	}
}

func TestAutoSummaryDegradesWhenRetainedTurnsTooLarge(t *testing.T) {
	manager := contextview.NewManager(contextview.Config{Budget: contextview.NewBudget(32_768, 4096)})
	populateTurns(manager, 6, 10_000)
	tools := &testTools{}
	summaries := 0
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		if req.System == summarySystemPrompt {
			summaries++
			return model.Response{Message: textMessage(model.RoleAssistant, "<summary>摘要</summary>"), StopReason: model.StopEndTurn}, nil
		}
		return finalResponse(), nil
	})
	if _, err := NewConversation(Options{
		Client: client, Tools: tools, Model: "model", ContextView: manager, MaxTurns: 2,
	}).Run(context.Background(), "new-task", nil); err != nil {
		t.Fatal(err)
	}
	if summaries < 2 {
		t.Fatalf("最近 5 轮本身过大时应继续降级压缩：%d", summaries)
	}
	if stats := manager.Stats("", nil); stats.Turns > 1 {
		t.Fatalf("降级后应只保留当前轮：%+v", stats)
	}
}
