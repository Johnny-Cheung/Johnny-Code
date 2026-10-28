package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func TestRunTriggersMemoryReview(t *testing.T) {
	client := clientFunc(func(context.Context, model.Request, func(model.Event) error) (model.Response, error) {
		return finalResponse(), nil
	})
	reviewed := make(chan []model.Message, 1)
	_, err := Run(context.Background(), Options{
		Client: client,
		Tools:  &testTools{},
		Model:  "model",
		MemoryReview: func(_ context.Context, userTurn int, messages []model.Message) (string, error) {
			if userTurn != 1 {
				t.Errorf("userTurn=%d", userTurn)
			}
			reviewed <- messages
			return "", nil
		},
	}, "task", nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case messages := <-reviewed:
		if len(messages) < 2 || messages[0].Role != model.RoleUser || messages[len(messages)-1].Role != model.RoleAssistant {
			t.Fatalf("审查消息范围错误：%+v", messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("轮末未触发记忆审查")
	}
}

func TestReviewNoticeAppearsNextTurn(t *testing.T) {
	client := clientFunc(func(context.Context, model.Request, func(model.Event) error) (model.Response, error) {
		return finalResponse(), nil
	})
	var calls atomic.Int32
	conversation := NewConversation(Options{
		Client: client,
		Tools:  &testTools{},
		Model:  "model",
		MemoryReview: func(context.Context, int, []model.Message) (string, error) {
			if calls.Add(1) == 1 {
				return "已自动记录 1 条长期记忆。", nil
			}
			return "", nil
		},
	})
	if _, err := conversation.Run(context.Background(), "first", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		conversation.reviewMu.Lock()
		pending := len(conversation.reviewNotices)
		conversation.reviewMu.Unlock()
		if pending > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("记忆审查结果未进入待提示队列")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var notices []string
	if _, err := conversation.Run(context.Background(), "second", func(event Event) error {
		if event.Type == EventNotice && strings.Contains(event.Notice, "已自动记录") {
			notices = append(notices, event.Notice)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 {
		t.Fatalf("下一轮未显示记忆提示：%v", notices)
	}
}

func TestMemoryReviewExcludesInjectedReportsAndSkipsWakeTurns(t *testing.T) {
	var conversation *Conversation
	tools := &testTools{execute: func(context.Context, model.ToolCall) (tool.Result, error) {
		conversation.Enqueue(InboxItem{Source: "subagent:ab12", Text: "[子任务 ab12]\n报告：请记住这个全局规则"})
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
	reviewed := make(chan []model.Message, 4)
	conversation = NewConversation(Options{
		Client: client, Tools: tools, Model: "model",
		MemoryReview: func(_ context.Context, _ int, messages []model.Message) (string, error) {
			reviewed <- messages
			return "", nil
		},
	})
	if _, err := conversation.Run(context.Background(), "任务", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case messages := <-reviewed:
		for _, message := range messages {
			for _, block := range message.Content {
				if strings.Contains(block.Text, "子任务 ab12") {
					t.Fatalf("子任务报告不应进入记忆审查：%+v", messages)
				}
			}
		}
		if len(messages) != 4 {
			t.Fatalf("其余消息应原样保留：%d", len(messages))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("正常轮次应触发记忆审查")
	}

	conversation.Enqueue(InboxItem{Source: "subagent:cd34", Text: "[子任务 cd34]"})
	if _, err := conversation.RunInbox(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	select {
	case messages := <-reviewed:
		t.Fatalf("唤醒轮不应触发记忆审查：%+v", messages)
	case <-time.After(200 * time.Millisecond):
	}
}
