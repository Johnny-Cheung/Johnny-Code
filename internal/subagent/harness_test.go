package subagent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/skill"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

type clientFunc func(context.Context, model.Request, func(model.Event) error) (model.Response, error)

func (f clientFunc) Stream(ctx context.Context, req model.Request, emit func(model.Event) error) (model.Response, error) {
	return f(ctx, req, emit)
}

func callResponse(id, name, arguments string) model.Response {
	return model.Response{
		Message:    model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{Type: model.BlockToolCall, ToolCall: &model.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(arguments)}}}},
		StopReason: model.StopToolUse,
	}
}

func textResponse(text string) model.Response {
	return model.Response{
		Message:    model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{Type: model.BlockText, Text: text}}},
		StopReason: model.StopEndTurn,
	}
}

func isChild(req model.Request) bool {
	if strings.HasPrefix(req.System, "CHILD-SYSTEM") {
		return true
	}
	for _, message := range req.Messages {
		if message.Role == model.RoleUser && len(message.Content) > 0 && strings.HasPrefix(message.Content[0].Text, "[子任务指令]") {
			return true
		}
	}
	return false
}

// harness 把真实的 Manager、task 工具和父会话接在一起，模型由测试脚本扮演。
type harness struct {
	t       *testing.T
	dir     string
	manager *Manager
	parent  *agent.Conversation
	respond func(ctx context.Context, req model.Request) (model.Response, error)
	gate    *tool.ApprovalGate

	mu       sync.Mutex
	events   []Event
	requests []model.Request
}

func (h *harness) handle(ctx context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
	h.mu.Lock()
	h.requests = append(h.requests, req)
	respond := h.respond
	h.mu.Unlock()
	return respond(ctx, req)
}

func (h *harness) onEvent(event Event) {
	h.mu.Lock()
	h.events = append(h.events, event)
	h.mu.Unlock()
}

func (h *harness) eventsOf(kind EventType) []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	var matched []Event
	for _, event := range h.events {
		if event.Type == kind {
			matched = append(matched, event)
		}
	}
	return matched
}

func (h *harness) childRequests() []model.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	var matched []model.Request
	for _, req := range h.requests {
		if isChild(req) {
			matched = append(matched, req)
		}
	}
	return matched
}

func (h *harness) waitFor(what string, condition func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			h.t.Fatalf("等待超时：%s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) waitEnded(count int) []Event {
	h.t.Helper()
	h.waitFor("子任务结束事件", func() bool { return len(h.eventsOf(EventEnded)) >= count })
	return h.eventsOf(EventEnded)
}

func (h *harness) runParent(prompt string) {
	h.t.Helper()
	if _, err := h.parent.Run(context.Background(), prompt, nil); err != nil {
		h.t.Fatalf("主会话运行失败：%v", err)
	}
}

func (h *harness) path(parts ...string) string {
	return filepath.Join(append([]string{h.dir}, parts...)...)
}

type harnessOptions struct {
	mode      Mode
	configure func(*Config)
	view      *contextview.Manager
}

func newHarness(t *testing.T, options harnessOptions) *harness {
	t.Helper()
	if options.mode == "" {
		options.mode = ModeAllow
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := tool.NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	base, err := tool.NewDefaultRegistryWithSkill(runtime, skill.Discover(dir))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := tool.NewProjectRules(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, dir: dir, gate: tool.NewApprovalGate()}
	client := clientFunc(h.handle)
	cfg := Config{
		WorkDir: dir, SessionID: "parent", InteractionMode: string(options.mode), Client: client, Model: "model",
		Toolbox: Toolbox{Runtime: runtime, Registry: base, Mode: options.mode, Rules: rules, Gate: h.gate},
		System:  func(Info) string { return "CHILD-SYSTEM" },
		OnEvent: h.onEvent,
	}
	if options.configure != nil {
		options.configure(&cfg)
	}
	h.manager, err = NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	taskTool, err := tool.NewTaskTool(runtime, h.manager)
	if err != nil {
		t.Fatal(err)
	}
	full, err := base.With(taskTool)
	if err != nil {
		t.Fatal(err)
	}
	var tools agent.Tools = full
	switch options.mode {
	case ModeDefault:
		tools, err = tool.NewDefault(full, runtime, rules, h.gate)
	case ModeReadOnly:
		tools, err = tool.NewReadOnly(full, runtime)
	}
	if err != nil {
		t.Fatal(err)
	}
	if tools, err = h.manager.GuardMain(tools); err != nil {
		t.Fatal(err)
	}
	h.parent = agent.NewConversation(agent.Options{Client: client, Tools: tools, Model: "model", System: "PARENT-SYSTEM", MaxTurns: 10, ContextView: options.view})
	h.manager.SetParent(h.parent, tools)
	t.Cleanup(h.manager.Close)
	return h
}
