package contextview

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

func toolCallMessage(ids ...string) model.Message {
	message := model.Message{Role: model.RoleAssistant}
	for _, id := range ids {
		message.Content = append(message.Content, model.ContentBlock{
			Type:     model.BlockToolCall,
			ToolCall: &model.ToolCall{ID: id, Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
		})
	}
	return message
}

func TestAppendUserStaysInCurrentTurn(t *testing.T) {
	manager := NewManager(Config{})
	manager.BeginTurn(1, userMessage("任务"))
	manager.AppendAssistant(assistantMessage("好"))
	manager.AppendUser(userMessage("[子任务 ab12 状态：completed]"))
	messages := manager.Messages()
	if len(messages) != 3 || messages[2].Role != model.RoleUser || messages[2].Content[0].Text != "[子任务 ab12 状态：completed]" {
		t.Fatalf("注入消息位置错误：%+v", messages)
	}
	if manager.turns[0].End != 3 || len(manager.turns) != 1 {
		t.Fatalf("注入不应开启新轮次：%+v", manager.turns)
	}
	manager.FinishTurn()
	if len(manager.Messages()) != 3 {
		t.Fatal("轮末评估不应丢失注入消息")
	}
}

func TestForkCutsAtLastCompleteBoundary(t *testing.T) {
	manager := NewManager(Config{WorkDir: t.TempDir(), SessionID: "parent"})
	manager.BeginTurn(1, userMessage("第一轮"))
	manager.AppendAssistant(assistantMessage("第一轮完成"))
	manager.FinishTurn()
	manager.BeginTurn(2, userMessage("第二轮"))
	first := toolCallMessage("a", "task-1", "c")
	manager.AppendAssistant(first)
	manager.AppendToolResult(2, *first.Content[0].ToolCall, model.ToolResult{ToolCallID: "a", Content: "ok"})

	child := manager.Fork("child-session")
	messages := child.Messages()
	if len(messages) != 3 || messages[2].Content[0].Text != "第二轮" {
		t.Fatalf("fork 应截到进行中的 assistant 消息之前：%+v", messages)
	}
	for _, message := range messages {
		for _, block := range message.Content {
			if block.ToolCall != nil || block.ToolResult != nil {
				t.Fatalf("fork 起点不应含工具调用或结果：%+v", message)
			}
		}
	}
	if len(child.turns) != 2 || child.turns[1].End != 3 || len(child.turns[1].Results) != 0 {
		t.Fatalf("轮次信息未截断：%+v", child.turns)
	}
	if len(manager.Messages()) != 5 {
		t.Fatal("fork 不应改动父视图")
	}
	child.BeginTurn(3, userMessage("子任务指令"))
	child.AppendAssistant(assistantMessage("子任务完成"))
	if len(manager.Messages()) != 5 || len(child.Messages()) != 5 {
		t.Fatal("父子视图应相互独立")
	}
}

func TestForkKeepsCompleteHistoryAndSummary(t *testing.T) {
	manager := NewManager(Config{})
	for turn := 1; turn <= 3; turn++ {
		manager.BeginTurn(turn, userMessage("任务"))
		call := toolCallMessage("call-" + string(rune('0'+turn)))
		manager.AppendAssistant(call)
		manager.AppendToolResult(turn, *call.Content[0].ToolCall, model.ToolResult{ToolCallID: call.Content[0].ToolCall.ID, Content: "结果"})
		manager.AppendAssistant(assistantMessage("完成"))
		manager.FinishTurn()
	}
	if !manager.ApplySummary("摘要正文", 1) {
		t.Fatal("摘要应成功")
	}
	child := manager.Fork("child")
	if child.SummaryText() != "摘要正文" || child.SummaryCovered() != manager.SummaryCovered() {
		t.Fatalf("摘要状态未带走：%q %d", child.SummaryText(), child.SummaryCovered())
	}
	if len(child.Messages()) != len(manager.Messages()) || len(child.turns) != len(manager.turns) {
		t.Fatal("完整历史应整体复制")
	}
	if child.Messages()[0].Content[0].Text != "摘要正文" {
		t.Fatal("摘要消息应保留在最前面")
	}
	child.BeginTurn(4, userMessage("子任务指令"))
	if child.CurrentUserTurn() != 4 || manager.CurrentUserTurn() != 3 {
		t.Fatal("轮次编号应各自独立")
	}
}

func TestForkUsesSeparateArtifactNamespace(t *testing.T) {
	dir := t.TempDir()
	manager := NewManager(Config{WorkDir: dir, SessionID: "parent"})
	child := manager.Fork("child")
	if !strings.HasSuffix(child.artifacts.Dir(), "/artifacts/child") || manager.artifacts.Dir() == child.artifacts.Dir() {
		t.Fatalf("存档命名空间应独立：%s %s", manager.artifacts.Dir(), child.artifacts.Dir())
	}
	if child.Budget() != manager.Budget() || child.PreviewBytes() != manager.PreviewBytes() {
		t.Fatal("预算和预览上限应沿用父视图")
	}
}
