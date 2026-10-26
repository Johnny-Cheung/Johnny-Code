package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
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
