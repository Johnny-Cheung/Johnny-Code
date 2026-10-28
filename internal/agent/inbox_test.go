package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func TestInboxInjectsBeforeNextRequestInSameTurn(t *testing.T) {
	var conversation *Conversation
	tools := &testTools{execute: func(context.Context, model.ToolCall) (tool.Result, error) {
		conversation.Enqueue(InboxItem{Source: "subagent:ab12", Text: "[子任务 ab12 状态：completed]\n报告正文"})
		return tool.Result{Content: "ok"}, nil
	}}
	requests := 0
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		requests++
		switch requests {
		case 1:
			if len(req.Messages) != 1 {
				t.Fatalf("第一次请求不应有注入：%+v", req.Messages)
			}
			return toolResponse("call-1"), nil
		case 2:
			last := req.Messages[len(req.Messages)-1]
			if len(req.Messages) != 4 || req.Messages[2].Role != model.RoleTool || last.Role != model.RoleUser || last.Content[0].Text != "[子任务 ab12 状态：completed]\n报告正文" {
				t.Fatalf("结果应在工具结果之后注入当前轮：%+v", req.Messages)
			}
			return finalResponse(), nil
		}
		t.Fatal("不应有更多请求")
		return model.Response{}, nil
	})
	conversation = NewConversation(Options{Client: client, Tools: tools, Model: "model"})
	var injected []Event
	result, err := conversation.Run(context.Background(), "任务", func(event Event) error {
		if event.Type == EventInject {
			injected = append(injected, event)
		}
		return nil
	})
	if err != nil || result.Status != StatusCompleted || result.UserTurn != 1 {
		t.Fatalf("运行失败：%+v %v", result, err)
	}
	if len(injected) != 1 || len(injected[0].Sources) != 1 || injected[0].Sources[0] != "subagent:ab12" || injected[0].Message == nil || injected[0].Turn != 2 {
		t.Fatalf("注入事件错误：%+v", injected)
	}
	if conversation.InboxLen() != 0 {
		t.Fatal("注入后收件箱应清空")
	}
}

func TestInboxItemsEnqueuedWhileIdleJoinNextRun(t *testing.T) {
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		if len(req.Messages) != 2 || req.Messages[0].Content[0].Text != "新任务" || req.Messages[1].Content[0].Text != "空闲时到达的结果" {
			t.Fatalf("空闲期间到达的结果应跟在新任务之后：%+v", req.Messages)
		}
		return finalResponse(), nil
	})
	conversation := NewConversation(Options{Client: client, Tools: &testTools{}, Model: "model"})
	conversation.Enqueue(InboxItem{Source: "subagent:1", Text: "空闲时到达的结果"})
	conversation.Enqueue(InboxItem{Source: "subagent:2", Text: "   "})
	if conversation.InboxLen() != 1 {
		t.Fatal("空白内容不应入队")
	}
	if _, err := conversation.Run(context.Background(), "新任务", nil); err != nil {
		t.Fatal(err)
	}
}

func TestInboxMergeIsBoundedAndKeepsOrder(t *testing.T) {
	big := func(label string) string { return label + strings.Repeat("x", 10<<10) }
	requests := 0
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		requests++
		switch requests {
		case 1:
			injected := req.Messages[1].Content[0].Text
			if len(req.Messages) != 2 || !strings.HasPrefix(injected, "A") || !strings.Contains(injected, "\n\nB") || strings.Contains(injected, "C") {
				t.Fatalf("第一次只应合并 A、B：%d 条，长度 %d", len(req.Messages), len(injected))
			}
			if len(injected) > MaxInjectBytes+2 {
				t.Fatalf("合并注入超过上限：%d", len(injected))
			}
			return toolResponse("call-1"), nil
		default:
			last := req.Messages[len(req.Messages)-1]
			if last.Role != model.RoleUser || !strings.HasPrefix(last.Content[0].Text, "C") {
				t.Fatalf("剩余条目应在下一次请求注入：%+v", last)
			}
			return finalResponse(), nil
		}
	})
	conversation := NewConversation(Options{Client: client, Tools: &testTools{}, Model: "model"})
	for _, label := range []string{"A", "B", "C"} {
		conversation.Enqueue(InboxItem{Source: label, Text: big(label)})
	}
	if _, err := conversation.Run(context.Background(), "任务", nil); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || conversation.InboxLen() != 0 {
		t.Fatalf("请求数或收件箱状态错误：%d %d", requests, conversation.InboxLen())
	}
}

func TestRunInboxStartsWakeTurnFromInbox(t *testing.T) {
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		if len(req.Messages) != 3 || req.Messages[2].Role != model.RoleUser || req.Messages[2].Content[0].Text != "[子任务 1]\n\n[子任务 2]" {
			t.Fatalf("唤醒轮应以合并后的收件箱内容为起点：%+v", req.Messages)
		}
		return finalResponse(), nil
	})
	conversation := NewConversation(Options{Client: client, Tools: &testTools{}, Model: "model"})
	conversation.messages = []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.BlockText, Text: "旧任务"}}}, finalResponse().Message}
	conversation.view.BeginTurn(1, conversation.messages[0])
	conversation.view.AppendAssistant(conversation.messages[1])
	conversation.view.FinishTurn()
	conversation.userTurn = 1
	conversation.Enqueue(InboxItem{Source: "subagent:1", Text: "[子任务 1]"})
	conversation.Enqueue(InboxItem{Source: "subagent:2", Text: "[子任务 2]"})
	var start *Event
	result, err := conversation.RunInbox(context.Background(), func(event Event) error {
		if event.Type == EventStart {
			copied := event
			start = &copied
		}
		return nil
	})
	if err != nil || result.UserTurn != 2 || result.Status != StatusCompleted {
		t.Fatalf("唤醒轮失败：%+v %v", result, err)
	}
	if start == nil || len(start.Sources) != 2 || start.Sources[1] != "subagent:2" {
		t.Fatalf("唤醒轮起点应标明来源：%+v", start)
	}
	if _, err := conversation.RunInbox(context.Background(), nil); err == nil || conversation.userTurn != 2 {
		t.Fatalf("空收件箱不应产生新轮次：%v %d", err, conversation.userTurn)
	}
}

func TestRunInboxRestoresItemsWhenTurnCannotStart(t *testing.T) {
	conversation := NewConversation(Options{Tools: &testTools{}, Model: "model"})
	conversation.Enqueue(InboxItem{Source: "subagent:1", Text: "结果"})
	if _, err := conversation.RunInbox(context.Background(), nil); err == nil {
		t.Fatal("缺少模型客户端应失败")
	}
	if conversation.InboxLen() != 1 {
		t.Fatal("轮次未开始时应把条目放回收件箱")
	}
}

func TestInboxEnqueueIsSafeDuringRun(t *testing.T) {
	var conversation *Conversation
	var wg sync.WaitGroup
	tools := &testTools{execute: func(context.Context, model.ToolCall) (tool.Result, error) {
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				conversation.Enqueue(InboxItem{Source: "subagent", Text: "r"})
			}()
		}
		wg.Wait()
		return tool.Result{Content: "ok"}, nil
	}}
	requests := 0
	client := clientFunc(func(context.Context, model.Request, func(model.Event) error) (model.Response, error) {
		requests++
		if requests == 1 {
			return toolResponse("call-1"), nil
		}
		return finalResponse(), nil
	})
	conversation = NewConversation(Options{Client: client, Tools: tools, Model: "model"})
	if _, err := conversation.Run(context.Background(), "任务", nil); err != nil {
		t.Fatal(err)
	}
	if conversation.InboxLen() != 0 {
		t.Fatal("并发入队的条目应全部注入")
	}
}
