package subagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

// childBashResponder 让每个子任务先执行一条 bash，再在拿到工具结果后结束。
// denied 非空时记录工具结果内容，用于核验只读子任务没有泄露文件。
func childBashResponder(command string, denied *string, mu *sync.Mutex) func(context.Context, model.Request) (model.Response, error) {
	raw, _ := json.Marshal(map[string]any{"command": command})
	return func(_ context.Context, req model.Request) (model.Response, error) {
		if !isChild(req) {
			return textResponse("好"), nil
		}
		last := req.Messages[len(req.Messages)-1]
		if len(last.Content) > 0 && last.Content[0].ToolResult != nil {
			if denied != nil && mu != nil {
				mu.Lock()
				*denied = last.Content[0].ToolResult.Content
				mu.Unlock()
			}
			return textResponse("完成"), nil
		}
		return callResponse("child-bash", "bash", string(raw)), nil
	}
}

func countingAsk(h *harness, decision tool.ApprovalDecision) func() int {
	var mu sync.Mutex
	count := 0
	h.gate.Set(func(context.Context, tool.ApprovalRequest) (tool.ApprovalDecision, error) {
		mu.Lock()
		count++
		mu.Unlock()
		return decision, nil
	})
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

func TestSessionGrantSharedAcrossWriteChildren(t *testing.T) {
	h := newHarness(t, harnessOptions{mode: ModeDefault})
	for _, dir := range []string{"pkg-a", "pkg-b"} {
		if err := os.MkdirAll(h.path(dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.respond = childBashResponder("mkdir -p child-shared", nil, nil)
	asks := countingAsk(h, tool.ApprovalDecision{Allow: true, Session: true})

	if _, err := h.manager.Spawn(context.Background(), writeRequest(h, "甲", "pkg-a")); err != nil {
		t.Fatal(err)
	}
	if ended := h.waitEnded(1); ended[0].Status != agent.StatusCompleted {
		t.Fatalf("第一个子任务应完成：%+v", ended[0])
	}
	if asks() != 1 {
		t.Fatalf("第一个子任务应恰好审批一次：%d", asks())
	}

	if _, err := h.manager.Spawn(context.Background(), writeRequest(h, "乙", "pkg-b")); err != nil {
		t.Fatal(err)
	}
	for _, ended := range h.waitEnded(2) {
		if ended.Status != agent.StatusCompleted {
			t.Fatalf("子任务应完成：%+v", ended)
		}
	}
	if asks() != 1 {
		t.Fatalf("同一命令的第二个写入子任务不应再审批：%d", asks())
	}
	if info, err := os.Stat(h.path("child-shared")); err != nil || !info.IsDir() {
		t.Fatalf("共享会话授权后命令应执行：%v", err)
	}
}

func TestSessionGrantSharedAcrossWorkflowSteps(t *testing.T) {
	h := newHarness(t, harnessOptions{mode: ModeDefault})
	for _, dir := range []string{"pkg1", "pkg2"} {
		if err := os.MkdirAll(h.path(dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.respond = childBashResponder("mkdir -p workflow-shared", nil, nil)
	asks := countingAsk(h, tool.ApprovalDecision{Allow: true, Session: true})
	h.startWorkflow("一", "二")

	first := []tool.SpawnRequest{{
		Description: "甲", Prompt: "任务：甲", Kind: tool.SpawnFresh,
		Access: tool.SpawnWrite, Paths: []string{h.path("pkg1")},
	}}
	if _, err := h.manager.RunWorkflowStep(context.Background(), 1, first); err != nil {
		t.Fatal(err)
	}
	if ended := h.waitEnded(1); ended[0].Status != agent.StatusCompleted {
		t.Fatalf("第一步任务应完成：%+v", ended[0])
	}
	if asks() != 1 {
		t.Fatalf("第一步应恰好审批一次：%d", asks())
	}
	h.waitFor("第一步屏障", func() bool { return len(h.eventsOf(EventStepEnded)) == 1 })

	second := []tool.SpawnRequest{{
		Description: "乙", Prompt: "任务：乙", Kind: tool.SpawnFresh,
		Access: tool.SpawnWrite, Paths: []string{h.path("pkg2")},
	}}
	if _, err := h.manager.RunWorkflowStep(context.Background(), 2, second); err != nil {
		t.Fatal(err)
	}
	for _, ended := range h.waitEnded(2) {
		if ended.Status != agent.StatusCompleted {
			t.Fatalf("工作流任务应完成：%+v", ended)
		}
	}
	if asks() != 1 {
		t.Fatalf("后续 workflow 任务应复用会话授权：%d", asks())
	}
	if info, err := os.Stat(h.path("workflow-shared")); err != nil || !info.IsDir() {
		t.Fatalf("workflow 任务应执行共享命令：%v", err)
	}
}

func TestReadChildIgnoresSessionGrants(t *testing.T) {
	h := newHarness(t, harnessOptions{mode: ModeDefault})
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("TOP-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.gate.Session().Store(
		[]tool.CommandGrant{{WorkDir: h.dir, Argv: []string{"cat", outside}}},
		[]tool.PathRule{{Path: resolved, Kind: tool.PathKindFile}},
	); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var denied string
	h.respond = childBashResponder("cat "+outside, &denied, &mu)
	asks := countingAsk(h, tool.ApprovalDecision{Allow: true, Session: true})
	if _, err := h.manager.Spawn(context.Background(), freshReadRequest("读")); err != nil {
		t.Fatal(err)
	}
	h.waitEnded(1)
	if asks() != 0 {
		t.Fatalf("read 子任务不应进入审批：%d", asks())
	}
	mu.Lock()
	content := denied
	mu.Unlock()
	if content == "" {
		t.Fatal("只读子任务应收到路径拒绝结果")
	}
	if strings.Contains(content, "TOP-SECRET") {
		t.Fatalf("read 子任务不应因会话授权读取外部文件：%q", content)
	}
	if commands, paths := h.gate.Session().Len(); commands != 1 || paths != 1 {
		t.Fatalf("read 子任务不应修改共享授权：%d %d", commands, paths)
	}
}
