package subagent

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func requestTexts(req model.Request) []string {
	var texts []string
	for _, message := range req.Messages {
		for _, block := range message.Content {
			switch {
			case block.ToolCall != nil:
				texts = append(texts, "call:"+block.ToolCall.Name)
			case block.ToolResult != nil:
				texts = append(texts, "result:"+block.ToolResult.Content)
			default:
				texts = append(texts, block.Text)
			}
		}
	}
	return texts
}

func TestForkTaskStartsFromParentSnapshotAndRejectsNestedTools(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	var mu sync.Mutex
	parentCalls, childCalls := 0, 0
	h.respond = func(_ context.Context, req model.Request) (model.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		if isChild(req) {
			childCalls++
			if childCalls == 1 {
				texts := requestTexts(req)
				if len(texts) != 4 || strings.Join(texts[:3], "|") != "第一轮|完成|第二轮" || !strings.HasPrefix(texts[3], "[子任务指令]") || !strings.Contains(texts[3], "按我们讨论的方案实现") {
					t.Errorf("fork 起点应是父历史快照加子任务指令，且没有悬空调用：%v", texts)
				}
				if req.System != "PARENT-SYSTEM" || !strings.Contains(toolNames(req.Tools), "task") || !strings.Contains(toolNames(req.Tools), "write") {
					t.Errorf("fork 应沿用父系统提示和工具定义：%q %s", req.System, toolNames(req.Tools))
				}
				return callResponse("k1", "task", `{"description":"嵌套","prompt":"x","kind":"fresh"}`), nil
			}
			last := req.Messages[len(req.Messages)-1].Content[0].ToolResult
			if last == nil || !last.IsError || !strings.Contains(last.Content, "不能再派出") {
				t.Errorf("fork 子任务调用 task 应被明确拒绝：%+v", last)
			}
			return textResponse("分身完成"), nil
		}
		parentCalls++
		switch parentCalls {
		case 1:
			return textResponse("完成"), nil
		case 2:
			return callResponse("c1", "task", `{"description":"实现","prompt":"按我们讨论的方案实现","kind":"fork"}`), nil
		}
		return textResponse("已派出"), nil
	}
	h.runParent("第一轮")
	h.runParent("第二轮")
	ended := h.waitEnded(1)[0]
	if ended.Status != agent.StatusCompleted || ended.Requests != 2 || !strings.Contains(ended.Report, "分身完成") {
		t.Fatalf("fork 子任务应完成并回送报告：%+v", ended)
	}
	if len(h.eventsOf(EventStarted)) != 1 {
		t.Fatal("被拒绝的嵌套 task 不应启动新的子任务")
	}
	if got := h.parent.ContextStats().Turns; got != 2 {
		t.Fatalf("fork 不应改变父会话轮次：%d", got)
	}
}

func TestForkRejectedWhenParentContextIsLarge(t *testing.T) {
	view := contextview.NewManager(contextview.Config{Budget: contextview.NewBudget(40_000, 100)})
	h := newHarness(t, harnessOptions{view: view, configure: func(c *Config) { c.MaxOutputTokens = 100 }})
	h.respond = func(context.Context, model.Request) (model.Response, error) { return textResponse("好"), nil }
	h.runParent(strings.Repeat("长", 14000))
	request := tool.SpawnRequest{Description: "实现", Prompt: "做", Kind: tool.SpawnFork, Access: tool.SpawnWrite, Paths: []string{h.path("pkg")}}
	if _, err := h.manager.Spawn(context.Background(), request); err == nil || !strings.Contains(err.Error(), "fresh") {
		t.Fatalf("过长的父上下文应拒绝 fork 并建议 fresh：%v", err)
	}
	if h.manager.Running() != 0 || len(h.manager.claims.Owners()) != 0 {
		t.Fatal("拒绝 fork 不应遗留运行项或文件范围")
	}
}

func TestWriteTaskIsConfinedToDeclaredPaths(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	if err := os.MkdirAll(h.path("pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var mu sync.Mutex
	childCalls, mainCalls := 0, 0
	h.respond = func(_ context.Context, req model.Request) (model.Response, error) {
		if isChild(req) {
			mu.Lock()
			childCalls++
			n := childCalls
			mu.Unlock()
			switch n {
			case 1:
				return callResponse("w1", "write", `{"path":"pkg/a.txt","content":"ok"}`), nil
			case 2:
				return callResponse("w2", "write", `{"path":"other.txt","content":"no"}`), nil
			case 3:
				<-release
				return textResponse("写完了"), nil
			}
		}
		last := req.Messages[len(req.Messages)-1]
		if last.Role == model.RoleTool {
			return textResponse("好"), nil
		}
		mu.Lock()
		mainCalls++
		id := "m" + strconv.Itoa(mainCalls)
		mu.Unlock()
		return callResponse(id, "write", `{"path":"pkg/m.txt","content":"main"}`), nil
	}
	request := tool.SpawnRequest{Description: "写 pkg", Prompt: "写文件", Kind: tool.SpawnFresh, Access: tool.SpawnWrite, Paths: []string{h.path("pkg")}}
	id, err := h.manager.Spawn(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	h.waitFor("子任务第三次请求", func() bool { mu.Lock(); defer mu.Unlock(); return childCalls == 3 })
	overlap := request
	overlap.Paths = []string{h.path("pkg", "sub")}
	if _, err := h.manager.Spawn(context.Background(), overlap); err == nil || !strings.Contains(err.Error(), id) {
		t.Fatalf("重叠范围应被拒绝并指出占用的子任务：%v", err)
	}
	h.runParent("主 Agent 也想写 pkg")
	if _, err := os.Stat(h.path("pkg", "m.txt")); err == nil {
		t.Fatal("主 Agent 不应写入被子任务占用的路径")
	}
	if _, err := os.Stat(h.path("pkg", "a.txt")); err != nil {
		t.Fatalf("子任务应能写自己的范围：%v", err)
	}
	if _, err := os.Stat(h.path("other.txt")); err == nil {
		t.Fatal("子任务不应写范围外的文件")
	}
	close(release)
	h.waitEnded(1)
	if len(h.manager.claims.Owners()) != 0 {
		t.Fatal("子任务结束后应释放文件范围")
	}
	h.runParent("现在可以写了")
	if _, err := os.Stat(h.path("pkg", "m.txt")); err != nil {
		t.Fatalf("释放后主 Agent 应能写入：%v", err)
	}
}

func TestReadAccessAndReadOnlyParentNeverWrite(t *testing.T) {
	t.Run("只读父会话拒绝写入类子任务", func(t *testing.T) {
		h := newHarness(t, harnessOptions{mode: ModeReadOnly})
		h.respond = func(context.Context, model.Request) (model.Response, error) { return textResponse("好"), nil }
		write := tool.SpawnRequest{Description: "写", Prompt: "写", Kind: tool.SpawnFresh, Access: tool.SpawnWrite, Paths: []string{h.path("pkg")}}
		if _, err := h.manager.Spawn(context.Background(), write); err == nil || !strings.Contains(err.Error(), "只读模式") {
			t.Fatalf("只读模式应拒绝 access=write：%v", err)
		}
		if _, err := h.manager.Spawn(context.Background(), freshReadRequest("读")); err != nil {
			t.Fatalf("只读模式仍可派出 access=read：%v", err)
		}
		h.waitEnded(1)
	})
	for _, kind := range []tool.SpawnKind{tool.SpawnFresh, tool.SpawnFork} {
		t.Run(string(kind)+" read 在放行模式下也只读", func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			var mu sync.Mutex
			calls := 0
			var denied string
			h.respond = func(_ context.Context, req model.Request) (model.Response, error) {
				if !isChild(req) {
					return textResponse("好"), nil
				}
				mu.Lock()
				defer mu.Unlock()
				calls++
				if calls == 1 {
					return callResponse("w1", "write", `{"path":"x.txt","content":"no"}`), nil
				}
				if result := req.Messages[len(req.Messages)-1].Content[0].ToolResult; result != nil {
					denied = result.Content
				}
				return textResponse("结束"), nil
			}
			h.runParent("先有一轮")
			request := tool.SpawnRequest{Description: "读", Prompt: "试图写入", Kind: kind, Access: tool.SpawnRead}
			if _, err := h.manager.Spawn(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			h.waitEnded(1)
			if _, err := os.Stat(h.path("x.txt")); err == nil {
				t.Fatal("access=read 的子任务不应写文件")
			}
			if denied == "" {
				t.Fatal("写入尝试应得到明确的拒绝结果")
			}
		})
	}
}
