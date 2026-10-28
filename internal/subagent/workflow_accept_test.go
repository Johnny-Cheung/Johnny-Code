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

func TestReadOnlyParentRejectsWriteTasksInWorkflowStep(t *testing.T) {
	h := newHarness(t, harnessOptions{mode: ModeReadOnly})
	children := newScriptedChildren(h)
	h.startWorkflow("唯一的一步")
	mixed := []tool.SpawnRequest{freshReadRequest("读"), writeRequest(h, "写", "pkg")}
	if _, err := h.manager.RunWorkflowStep(context.Background(), 1, mixed); err == nil || !strings.Contains(err.Error(), "只读模式") {
		t.Fatalf("只读模式下含写入类任务的整步应被拒绝：%v", err)
	}
	if h.manager.Running() != 0 {
		t.Fatal("被拒绝的整步不应启动任务")
	}
	if _, err := h.runStep(1, "读"); err != nil {
		t.Fatalf("只读任务仍可启动：%v", err)
	}
	children.finish("读", "完成")
	h.waitEnded(1)
}

func TestWorkflowTasksRequestApprovalTogetherWithOrigin(t *testing.T) {
	h := newHarness(t, harnessOptions{mode: ModeDefault})
	for _, dir := range []string{"pkg1", "pkg2"} {
		if err := os.MkdirAll(h.path(dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	h.respond = func(_ context.Context, req model.Request) (model.Response, error) {
		if !isChild(req) {
			return textResponse("好"), nil
		}
		if result := req.Messages[len(req.Messages)-1].Content[0].ToolResult; result != nil {
			return textResponse("做完了"), nil
		}
		mu.Lock()
		defer mu.Unlock()
		dir := "pkg1"
		if taskLabel(requestTexts(req)) == "乙" {
			dir = "pkg2"
		}
		return callResponse("b-"+dir, "bash", `{"command":"mkdir `+dir+`/new"}`), nil
	}
	asked := make(chan tool.ApprovalRequest, 2)
	release := make(chan struct{})
	h.gate.Set(func(ctx context.Context, request tool.ApprovalRequest) (tool.ApprovalDecision, error) {
		asked <- request
		select {
		case <-release:
			return tool.ApprovalDecision{Allow: true}, nil
		case <-ctx.Done():
			return tool.ApprovalDecision{}, ctx.Err()
		}
	})
	h.startWorkflow("一", "二")
	step := []tool.SpawnRequest{
		{Description: "甲", Prompt: "任务：甲", Kind: tool.SpawnFresh, Access: tool.SpawnWrite, Paths: []string{h.path("pkg1")}},
		{Description: "乙", Prompt: "任务：乙", Kind: tool.SpawnFresh, Access: tool.SpawnWrite, Paths: []string{h.path("pkg2")}},
	}
	if _, err := h.manager.RunWorkflowStep(context.Background(), 1, step); err != nil {
		t.Fatal(err)
	}
	origins := map[string]bool{}
	for i := 0; i < 2; i++ {
		request := <-asked
		if request.Origin.ID == "" || request.Origin.Description == "" {
			t.Fatalf("每个任务的审批都应标明来源：%+v", request)
		}
		origins[request.Origin.ID] = true
	}
	if len(origins) != 2 {
		t.Fatalf("两个任务应各自请求审批：%v", origins)
	}
	h.waitFor("两个任务都在等待审批", func() bool {
		view, ok := h.manager.Workflow()
		if !ok || len(view.Steps[0].Active) != 2 {
			return false
		}
		return view.Steps[0].Active[0].State == StateWaitingApproval && view.Steps[0].Active[1].State == StateWaitingApproval
	})
	close(release)
	for _, ended := range h.waitEnded(2) {
		if ended.Status != agent.StatusCompleted {
			t.Fatalf("批准后任务应完成：%+v", ended)
		}
	}
	for _, dir := range []string{"pkg1", "pkg2"} {
		if _, err := os.Stat(h.path(dir, "new")); err != nil {
			t.Fatalf("批准后命令应执行：%v", err)
		}
	}
	if h.parent.InboxLen() != 1 {
		t.Fatalf("两个任务的报告应合并成一条：%d", h.parent.InboxLen())
	}
}
