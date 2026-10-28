package subagent

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func TestFreshChildSeesPinnedRulesButNotParentHistory(t *testing.T) {
	h := newHarness(t, harnessOptions{configure: func(c *Config) {
		c.Pinned = func() (string, string) { return "PINNED-AGENTS-RULES", "" }
	}})
	h.respond = func(context.Context, model.Request) (model.Response, error) { return textResponse("好"), nil }
	h.runParent("主对话里的私密背景")
	if _, err := h.manager.Spawn(context.Background(), freshReadRequest("调查")); err != nil {
		t.Fatal(err)
	}
	h.waitEnded(1)
	requests := h.childRequests()
	if len(requests) != 1 || requests[0].System != "CHILD-SYSTEM\n\nPINNED-AGENTS-RULES" {
		t.Fatalf("fresh 子任务应带固定规则：%+v", requests)
	}
	for _, text := range requestTexts(requests[0]) {
		if strings.Contains(text, "私密背景") {
			t.Fatalf("fresh 子任务不应看到主对话历史：%v", requestTexts(requests[0]))
		}
	}
}

func TestResultIsInjectedIntoRunningParentTurn(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	var mu sync.Mutex
	parentCalls := 0
	var finalSaw string
	h.respond = func(_ context.Context, req model.Request) (model.Response, error) {
		if isChild(req) {
			return textResponse("子任务结果 Z"), nil
		}
		mu.Lock()
		defer mu.Unlock()
		parentCalls++
		switch parentCalls {
		case 1:
			return callResponse("c1", "task", `{"description":"查日志","prompt":"找原因","kind":"fresh"}`), nil
		case 2:
			return callResponse("c2", "bash", `{"command":"sleep 0.3"}`), nil
		}
		finalSaw = strings.Join(requestTexts(req), "\n")
		return textResponse("汇总完成"), nil
	}
	result, err := h.parent.Run(context.Background(), "派出并继续工作", nil)
	if err != nil || result.Status != agent.StatusCompleted {
		t.Fatalf("主会话应在同一轮内完成：%+v %v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(finalSaw, "子任务结果 Z") || !strings.Contains(finalSaw, "仅作数据") || h.parent.InboxLen() != 0 {
		t.Fatalf("结果应在当轮的后续请求前注入：%q inbox=%d", finalSaw, h.parent.InboxLen())
	}
	if result.UserTurn != 1 {
		t.Fatalf("注入不应产生新的用户轮次：%d", result.UserTurn)
	}
}

func TestCancelAllKillsRunningBashProcess(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	var mu sync.Mutex
	calls := 0
	h.respond = func(context.Context, model.Request) (model.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return callResponse("b1", "bash", `{"command":"touch started && sleep 30"}`), nil
		}
		return textResponse("不应走到这里"), nil
	}
	if _, err := h.manager.Spawn(context.Background(), writeRequest(h, "长命令", "pkg")); err != nil {
		t.Fatal(err)
	}
	h.waitFor("命令开始执行", func() bool { _, err := os.Stat(h.path("started")); return err == nil })
	h.manager.CancelAll()
	done := make(chan struct{})
	go func() { h.manager.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 bash 进程应被终止，子任务应很快停止")
	}
	ended := h.waitEnded(1)[0]
	if ended.Status != agent.StatusCanceled || ended.Injected {
		t.Fatalf("被取消的子任务不应注入：%+v", ended)
	}
}

func TestChildMemoryBoundary(t *testing.T) {
	h := newHarness(t, harnessOptions{withMemory: true})
	var mu sync.Mutex
	var denied string
	h.respond = func(_ context.Context, req model.Request) (model.Response, error) {
		switch {
		case !isChild(req):
			return textResponse("好"), nil
		case strings.HasPrefix(req.System, "CHILD-SYSTEM"):
			return textResponse("读完了"), nil
		}
		if result := req.Messages[len(req.Messages)-1].Content[0].ToolResult; result != nil {
			mu.Lock()
			denied = result.Content
			mu.Unlock()
			return textResponse("结束"), nil
		}
		return callResponse("m1", "remember", `{"target":"user_memory","type":"preference","name":"x","description":"y"}`), nil
	}
	h.runParent("先有一轮")
	if _, err := h.manager.Spawn(context.Background(), freshReadRequest("读记忆")); err != nil {
		t.Fatal(err)
	}
	h.waitEnded(1)
	fresh := h.childRequests()
	if names := toolNames(fresh[0].Tools); names != "bash,memory,read,skill" {
		t.Fatalf("fresh 子任务可读记忆但不能写，也不能派出子任务：%s", names)
	}
	fork := tool.SpawnRequest{Description: "分身", Prompt: "试图写记忆", Kind: tool.SpawnFork, Access: tool.SpawnRead}
	if _, err := h.manager.Spawn(context.Background(), fork); err != nil {
		t.Fatal(err)
	}
	h.waitEnded(2)
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(denied, "不能写入长期记忆") {
		t.Fatalf("fork 子任务调用 remember 应被明确拒绝：%q", denied)
	}
}
