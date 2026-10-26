package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

func TestRunInjectsPinnedContextIntoSystem(t *testing.T) {
	var gotSystem string
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		gotSystem = req.System
		return finalResponse(), nil
	})
	pinned := "## 项目规则\n来源：/tmp/AGENTS.md\n\n- 测试用 go test ./..."
	_, err := Run(context.Background(), Options{
		Client: client,
		Tools:  &testTools{},
		Model:  "model",
		System: "base system",
		Pinned: func() (string, string) { return pinned, "" },
	}, "task", nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotSystem != "base system\n\n"+pinned {
		t.Fatalf("系统提示未包含固定上下文：\n%q", gotSystem)
	}
}

func TestPinnedWarningIsNotified(t *testing.T) {
	client := clientFunc(func(context.Context, model.Request, func(model.Event) error) (model.Response, error) {
		return finalResponse(), nil
	})
	var notices []string
	_, err := Run(context.Background(), Options{
		Client: client,
		Tools:  &testTools{},
		Model:  "model",
		System: "base",
		Pinned: func() (string, string) { return "", "规则文件已截断" },
	}, "task", func(event Event) error {
		if event.Type == EventNotice {
			notices = append(notices, event.Notice)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || notices[0] != "规则文件已截断" {
		t.Fatalf("警告事件错误：%v", notices)
	}
}

func TestPinnedContextRefreshesAfterCompactionPoint(t *testing.T) {
	text := "old rules"
	conversation := NewConversation(Options{
		Pinned: func() (string, string) { return text, "" },
	})
	if conversation.system() != "old rules" {
		t.Fatalf("初始固定上下文错误：%q", conversation.system())
	}
	text = "new rules"
	var notices []string
	if err := conversation.refreshPinned(func(event Event) error {
		if event.Type == EventNotice {
			notices = append(notices, event.Notice)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if conversation.system() != "new rules" {
		t.Fatalf("刷新后固定上下文错误：%q", conversation.system())
	}
	if len(notices) == 0 || !strings.Contains(strings.Join(notices, "；"), "已重新加载") {
		t.Fatalf("缺少刷新提示：%v", notices)
	}
}

func TestCompactRefreshesPinnedContext(t *testing.T) {
	manager := contextview.NewManager(contextview.Config{Budget: contextview.NewBudget(1_000_000, 4096)})
	populateTurns(manager, 6, 1000)
	providerCalls := 0
	client := clientFunc(func(_ context.Context, _ model.Request, _ func(model.Event) error) (model.Response, error) {
		return model.Response{
			Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
				Type: model.BlockText,
				Text: "<summary>测试摘要</summary>",
			}}},
			StopReason: model.StopEndTurn,
		}, nil
	})
	conversation := NewConversation(Options{
		Client:      client,
		Tools:       &testTools{},
		Model:       "model",
		System:      "base",
		ContextView: manager,
		Pinned: func() (string, string) {
			providerCalls++
			return fmt.Sprintf("pinned-%d", providerCalls), ""
		},
	})
	var notices []string
	if _, err := conversation.Compact(context.Background(), func(event Event) error {
		if event.Type == EventNotice {
			notices = append(notices, event.Notice)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if providerCalls != 2 {
		t.Fatalf("压缩后应重新加载固定上下文，调用次数=%d", providerCalls)
	}
	if conversation.system() != "base\n\npinned-2" {
		t.Fatalf("系统提示未使用刷新后的固定上下文：%q", conversation.system())
	}
	if !strings.Contains(strings.Join(notices, "；"), "已重新加载") {
		t.Fatalf("缺少刷新提示：%v", notices)
	}
}
