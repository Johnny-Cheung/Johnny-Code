package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

type reviewClientFunc func(context.Context, model.Request, func(model.Event) error) (model.Response, error)

func (f reviewClientFunc) Stream(ctx context.Context, request model.Request, emit func(model.Event) error) (model.Response, error) {
	return f(ctx, request, emit)
}

func reviewResponse(text string) model.Response {
	return model.Response{
		Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Type: model.BlockText,
			Text: text,
		}}},
		StopReason: model.StopEndTurn,
	}
}

func testReviewer(t *testing.T, client model.Client) (*Reviewer, *Store, *Rules, string) {
	t.Helper()
	home := t.TempDir()
	work := t.TempDir()
	store, err := NewStoreAt(home, work)
	if err != nil {
		t.Fatal(err)
	}
	rules := NewRules(home)
	agents := &Agents{HomeDir: home, WorkDir: work}
	reviewer := NewReviewer(client, "model", "session-test", store, rules, agents, "allow")
	reviewer.Timeout = time.Second
	reviewer.Attempts = 2
	reviewer.Backoff = time.Millisecond
	return reviewer, store, rules, home
}

func reviewMessages() []model.Message {
	return []model.Message{
		{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.BlockText, Text: "记住这个项目提交前运行 go test"}}},
		{Role: model.RoleAssistant, Content: []model.ContentBlock{{Type: model.BlockText, Text: "好的。"}}},
	}
}

func TestReviewerWritesProjectMemory(t *testing.T) {
	client := reviewClientFunc(func(_ context.Context, request model.Request, _ func(model.Event) error) (model.Response, error) {
		if !strings.Contains(request.System, "长期记忆审查器") {
			t.Fatalf("审查系统提示错误：%s", request.System)
		}
		if len(request.Messages) != 1 || !strings.Contains(request.Messages[0].Content[0].Text, "本轮对话") {
			t.Fatalf("审查输入错误：%+v", request.Messages)
		}
		return reviewResponse(`{"memories":[{"action":"create","scope":"project","type":"project","name":"go-test","description":"提交前运行 go test","body":"提交前运行 go test ./...","confidence":"high","reason":"用户明确说明"}],"user_agents":[],"project_agents_suggestions":[]}`), nil
	})
	reviewer, store, _, _ := testReviewer(t, client)
	notice, err := reviewer.Review(context.Background(), 1, reviewMessages())
	if err != nil || !strings.Contains(notice, "已自动记录 1 条长期记忆") {
		t.Fatalf("审查失败：%q %v", notice, err)
	}
	item, err := store.Read(ScopeProject, "go-test")
	if err != nil || item.Source != "session-test:turn-1" || !strings.Contains(item.Body, "go test") {
		t.Fatalf("写入记忆错误：%+v %v", item, err)
	}
}

func TestReviewerNoopAndRetry(t *testing.T) {
	var calls atomic.Int32
	client := reviewClientFunc(func(_ context.Context, _ model.Request, _ func(model.Event) error) (model.Response, error) {
		if calls.Add(1) == 1 {
			return reviewResponse("不是 JSON"), nil
		}
		return reviewResponse(`{"memories":[],"user_agents":[],"project_agents_suggestions":[]}`), nil
	})
	reviewer, _, _, _ := testReviewer(t, client)
	notice, err := reviewer.Review(context.Background(), 1, reviewMessages())
	if err != nil || notice != "" {
		t.Fatalf("no-op 错误：%q %v", notice, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("无效 JSON 应重试一次：%d", calls.Load())
	}
}

func TestReviewerWritesUserAgent(t *testing.T) {
	client := reviewClientFunc(func(_ context.Context, _ model.Request, _ func(model.Event) error) (model.Response, error) {
		return reviewResponse(`{"memories":[],"user_agents":[{"action":"create","name":"answer-zh","description":"所有回答使用中文","evidence":"用户：以后所有项目都用中文","confidence":"high"}],"project_agents_suggestions":[]}`), nil
	})
	reviewer, _, rules, _ := testReviewer(t, client)
	notice, err := reviewer.Review(context.Background(), 1, reviewMessages())
	if err != nil || !strings.Contains(notice, "已新增 1 条用户规则") {
		t.Fatalf("用户规则审查失败：%q %v", notice, err)
	}
	data, err := os.ReadFile(rules.Path())
	if err != nil || !strings.Contains(string(data), "`answer-zh`") {
		t.Fatalf("用户规则未写入：%s %v", data, err)
	}
}

func TestReviewerSkipsSecrets(t *testing.T) {
	client := reviewClientFunc(func(_ context.Context, _ model.Request, _ func(model.Event) error) (model.Response, error) {
		return reviewResponse(`{"memories":[{"action":"create","scope":"project","type":"project","name":"secret","description":"密钥","body":"JOHNNY_API_KEY=value","confidence":"high"}],"user_agents":[],"project_agents_suggestions":[]}`), nil
	})
	reviewer, store, _, _ := testReviewer(t, client)
	notice, err := reviewer.Review(context.Background(), 1, reviewMessages())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "跳过 1 条无效候选") {
		t.Fatalf("应提示跳过无效候选：%q", notice)
	}
	if _, err := store.Read(ScopeProject, "secret"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("密钥不应写入：%v", err)
	}
}

func TestReviewerCircuitBreaker(t *testing.T) {
	var calls atomic.Int32
	client := reviewClientFunc(func(_ context.Context, _ model.Request, _ func(model.Event) error) (model.Response, error) {
		calls.Add(1)
		return model.Response{}, errors.New("model down")
	})
	reviewer, _, _, _ := testReviewer(t, client)
	reviewer.Attempts = 1
	for i := 0; i < 3; i++ {
		if _, err := reviewer.Review(context.Background(), i+1, reviewMessages()); err == nil {
			t.Fatal("失败审查应返回错误")
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("调用次数错误：%d", calls.Load())
	}
	if _, err := reviewer.Review(context.Background(), 4, reviewMessages()); !errors.Is(err, errReviewPaused) {
		t.Fatalf("连续失败后应暂停：%v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("暂停后不应再调用模型：%d", calls.Load())
	}
}

func TestReviewerIgnoresProjectAgentSuggestions(t *testing.T) {
	client := reviewClientFunc(func(_ context.Context, _ model.Request, _ func(model.Event) error) (model.Response, error) {
		return reviewResponse(`{"memories":[],"user_agents":[],"project_agents_suggestions":["可以写入项目 AGENTS"]}`), nil
	})
	reviewer, _, _, home := testReviewer(t, client)
	notice, err := reviewer.Review(context.Background(), 1, reviewMessages())
	if err != nil || !strings.Contains(notice, "项目 AGENTS 建议未写入") {
		t.Fatalf("建议处理错误：%q %v", notice, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".johnny", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("审查不应写用户 AGENTS：%v", err)
	}
}
