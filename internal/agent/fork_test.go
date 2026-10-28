package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func TestForkStartsFromCompleteBoundaryWithDirective(t *testing.T) {
	var conversation *Conversation
	var childResult Result
	var childErr error
	const system = "主系统提示"
	requests := 0
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		requests++
		switch requests {
		case 1:
			return finalResponse(), nil
		case 2:
			return toolResponse("a", "fork-1"), nil
		case 3:
			if req.System != system+"\n\n固定规则" || len(req.Tools) != 1 {
				t.Fatalf("fork 应沿用父系统提示和工具定义：%q %d", req.System, len(req.Tools))
			}
			texts := make([]string, 0, len(req.Messages))
			for _, message := range req.Messages {
				for _, block := range message.Content {
					if block.ToolCall != nil || block.ToolResult != nil {
						t.Fatalf("fork 起点不应带悬空调用或结果：%+v", req.Messages)
					}
					texts = append(texts, block.Text)
				}
			}
			if strings.Join(texts, "|") != "第一轮|完成|第二轮|子任务指令" {
				t.Fatalf("fork 起点错误：%v", texts)
			}
			return finalResponse(), nil
		case 4:
			if len(req.Messages) != 6 || req.Messages[2].Content[0].Text != "第二轮" || req.Messages[5].Content[0].ToolResult.ToolCallID != "fork-1" {
				t.Fatalf("fork 不应改变父会话历史：%+v", req.Messages)
			}
			return finalResponse(), nil
		}
		t.Fatal("不应有更多请求")
		return model.Response{}, nil
	})
	childTools := &testTools{}
	tools := &testTools{execute: func(ctx context.Context, call model.ToolCall) (tool.Result, error) {
		if call.ID != "fork-1" {
			return tool.Result{Content: "ok"}, nil
		}
		child, err := conversation.Fork(ForkOptions{Tools: childTools, SessionID: "child-1", MaxTurns: 3})
		if err != nil {
			return tool.Result{}, err
		}
		defer child.Close()
		childResult, childErr = child.Run(ctx, "子任务指令", nil)
		return tool.Result{Content: "forked"}, nil
	}}
	conversation = NewConversation(Options{
		Client: client, Tools: tools, Model: "model", System: system,
		Pinned: func() (string, string) { return "固定规则", "" },
	})
	for _, prompt := range []string{"第一轮", "第二轮"} {
		if _, err := conversation.Run(context.Background(), prompt, nil); err != nil {
			t.Fatal(err)
		}
	}
	if childErr != nil || childResult.Status != StatusCompleted || childResult.UserTurn != 3 {
		t.Fatalf("子会话运行失败：%+v %v", childResult, childErr)
	}
	if requests != 4 {
		t.Fatalf("请求次数错误：%d", requests)
	}
}

func TestForkChildIsIsolatedFromParentInboxAndMemory(t *testing.T) {
	reviewed := false
	parent := NewConversation(Options{
		Client: clientFunc(func(context.Context, model.Request, func(model.Event) error) (model.Response, error) {
			return finalResponse(), nil
		}),
		Tools: &testTools{}, Model: "model",
		MemoryReview: func(context.Context, int, []model.Message) (string, error) {
			reviewed = true
			return "", nil
		},
	})
	parent.Enqueue(InboxItem{Source: "subagent:1", Text: "父会话收件箱"})
	child, err := parent.Fork(ForkOptions{Tools: &testTools{}, SessionID: "child-2"})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	if child.InboxLen() != 0 || parent.InboxLen() != 1 {
		t.Fatal("子会话不应共享父收件箱")
	}
	if _, err := child.Run(context.Background(), "指令", nil); err != nil {
		t.Fatal(err)
	}
	if reviewed {
		t.Fatal("子会话不应触发轮末记忆审查")
	}
}

func TestForkRejectsLargeParentContext(t *testing.T) {
	budget := contextview.NewBudget(20_000, 100)
	view := contextview.NewManager(contextview.Config{Budget: budget})
	client := clientFunc(func(context.Context, model.Request, func(model.Event) error) (model.Response, error) {
		return finalResponse(), nil
	})
	parent := NewConversation(Options{Client: client, Tools: &testTools{}, Model: "model", MaxOutputTokens: 100, ContextView: view})
	if _, err := parent.Run(context.Background(), strings.Repeat("长", 3000), nil); err != nil {
		t.Fatal(err)
	}
	before := len(view.Messages())
	if _, err := parent.Fork(ForkOptions{Tools: &testTools{}, SessionID: "child-3"}); !errors.Is(err, ErrForkTooLarge) {
		t.Fatalf("超过 50%% 预算应拒绝 fork：%v", err)
	}
	if len(view.Messages()) != before {
		t.Fatal("拒绝 fork 不应改动父视图")
	}
}

func TestForkRequiresChildTools(t *testing.T) {
	parent := NewConversation(Options{Tools: &testTools{}, Model: "model"})
	if _, err := parent.Fork(ForkOptions{SessionID: "child-4"}); err == nil {
		t.Fatal("缺少子会话工具应失败")
	}
}
