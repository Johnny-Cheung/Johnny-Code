package subagent

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func writeRequest(h *harness, description string, paths ...string) tool.SpawnRequest {
	absolute := make([]string, len(paths))
	for i, path := range paths {
		absolute[i] = h.path(path)
	}
	return tool.SpawnRequest{Description: description, Prompt: "做事", Kind: tool.SpawnFresh, Access: tool.SpawnWrite, Paths: absolute}
}

func approvalScript(h *harness, command string) {
	var mu sync.Mutex
	calls := 0
	h.respond = func(_ context.Context, req model.Request) (model.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return callResponse("b1", "bash", `{"command":"`+command+`"}`), nil
		}
		return textResponse("做完了"), nil
	}
}

func TestDefaultModeApprovalCarriesOriginAndShowsWaitingState(t *testing.T) {
	h := newHarness(t, harnessOptions{mode: ModeDefault})
	if err := os.MkdirAll(h.path("pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	approvalScript(h, "mkdir pkg/new")
	asked := make(chan tool.ApprovalRequest, 1)
	decide := make(chan bool)
	h.gate.Set(func(ctx context.Context, request tool.ApprovalRequest) (tool.ApprovalDecision, error) {
		asked <- request
		select {
		case allow := <-decide:
			return tool.ApprovalDecision{Allow: allow}, nil
		case <-ctx.Done():
			return tool.ApprovalDecision{}, ctx.Err()
		}
	})
	id, err := h.manager.Spawn(context.Background(), writeRequest(h, "建目录", "pkg"))
	if err != nil {
		t.Fatal(err)
	}
	request := <-asked
	if request.Origin.ID != id || request.Origin.Description != "建目录" || request.Tool != "bash" {
		t.Fatalf("审批请求应标明来自哪个子任务：%+v", request)
	}
	h.waitFor("等待审批状态", func() bool {
		snapshot := h.manager.Snapshot()
		return len(snapshot) == 1 && snapshot[0].State == StateWaitingApproval
	})
	decide <- true
	ended := h.waitEnded(1)[0]
	if ended.Status != agent.StatusCompleted {
		t.Fatalf("批准后子任务应完成：%+v", ended)
	}
	var states []State
	for _, event := range h.eventsOf(EventState) {
		states = append(states, event.Info.State)
	}
	if len(states) != 2 || states[0] != StateWaitingApproval || states[1] != StateRunning {
		t.Fatalf("状态应经历等待审批再回到运行：%v", states)
	}
	if _, err := os.Stat(h.path("pkg", "new")); err != nil {
		t.Fatalf("批准后命令应执行：%v", err)
	}
}

func TestCancelDropsPendingApproval(t *testing.T) {
	h := newHarness(t, harnessOptions{mode: ModeDefault})
	approvalScript(h, "mkdir denied")
	asked := make(chan struct{}, 1)
	h.gate.Set(func(ctx context.Context, request tool.ApprovalRequest) (tool.ApprovalDecision, error) {
		asked <- struct{}{}
		<-ctx.Done()
		return tool.ApprovalDecision{}, ctx.Err()
	})
	if _, err := h.manager.Spawn(context.Background(), writeRequest(h, "等审批", "pkg")); err != nil {
		t.Fatal(err)
	}
	<-asked
	if count := h.manager.CancelAll(); count != 1 {
		t.Fatalf("应取消等待审批的子任务：%d", count)
	}
	ended := h.waitEnded(1)[0]
	if ended.Status != agent.StatusCanceled || ended.Injected {
		t.Fatalf("取消后不应注入：%+v", ended)
	}
	if _, err := os.Stat(h.path("denied")); err == nil {
		t.Fatal("取消后待审批命令不应执行")
	}
}

func TestEndedEventSeesReleasedScopeAndQueuedReport(t *testing.T) {
	var h *harness
	var mu sync.Mutex
	var owners, inbox []int
	h = newHarness(t, harnessOptions{configure: func(c *Config) {
		c.OnEvent = func(event Event) {
			if event.Type == EventEnded {
				mu.Lock()
				owners = append(owners, len(h.manager.claims.Owners()))
				inbox = append(inbox, h.parent.InboxLen())
				mu.Unlock()
			}
			h.onEvent(event)
		}
	}})
	h.respond = func(context.Context, model.Request) (model.Response, error) { return textResponse("好"), nil }
	if _, err := h.manager.Spawn(context.Background(), writeRequest(h, "写", "pkg")); err != nil {
		t.Fatal(err)
	}
	h.waitEnded(1)
	mu.Lock()
	defer mu.Unlock()
	if len(owners) != 1 || owners[0] != 0 || inbox[0] != 1 {
		t.Fatalf("结束事件到达时应已释放范围并把报告放入收件箱：owners=%v inbox=%v", owners, inbox)
	}
}

func TestSpawnValidatesRequestAndManagerState(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.respond = func(context.Context, model.Request) (model.Response, error) { return textResponse("好"), nil }
	bad := []tool.SpawnRequest{
		{Description: "d", Prompt: "p", Kind: "clone", Access: tool.SpawnRead},
		{Description: "d", Prompt: "p", Kind: tool.SpawnFresh, Access: "admin"},
		{Description: "d", Prompt: "p", Kind: tool.SpawnFresh, Access: tool.SpawnWrite},
	}
	for _, request := range bad {
		if _, err := h.manager.Spawn(context.Background(), request); err == nil {
			t.Fatalf("无效请求应被拒绝：%+v", request)
		}
	}
	if h.manager.Running() != 0 {
		t.Fatal("无效请求不应启动子任务")
	}
	h.manager.Close()
	if _, err := h.manager.Spawn(context.Background(), freshReadRequest("关闭后")); err == nil || !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("关闭后不应再启动子任务：%v", err)
	}
}

func TestNewManagerRequiresInputs(t *testing.T) {
	if _, err := NewManager(Config{}); err == nil {
		t.Fatal("缺少配置应失败")
	}
}
